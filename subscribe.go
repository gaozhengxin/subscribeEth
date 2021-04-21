package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"time"
	"strings"
	"io/ioutil"
	//"math/big"

	"github.com/BurntSushi/toml"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/core/types"
)

type Config struct {
	Endpoint string
	Server string
	ContractAddresses []string
}

var s struct {
    FOO struct {
        Usernames_Passwords map[string]string
    }
}

var configFile string

var SwapoutTopic common.Hash = common.HexToHash("0x6b616089d04950dc06c45c6dd787d657980543f89651aec47924752c7d16c888")
var BTCSwapoutTopic common.Hash = common.HexToHash("0x9c92ad817e5474d30a4378deface765150479363a897b0590fbb12ae9d89396b")
var ERC20TransferTopic common.Hash = common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")

func init() {
	log.Root().SetHandler(log.StdoutHandler)
	flag.StringVar(&configFile, "config", "./config.toml", "config")
}

func LoadConfig() *Config {
	config := &Config{}
	if _, err := toml.DecodeFile(configFile, &config); err != nil {
		panic(err)
	}
	log.Info("Load config success", "config", config)
	return config
}

func main() {
	flag.Parse()

	config := LoadConfig()

	// 合约地址
	var addressesMap = make(map[common.Address]string)
	var serverMap = make(map[common.Address]string)
	var addresses = make([]common.Address, 0)

	var endpoint string =  config.Endpoint

	topics := make([][]common.Hash, 0)
	//topics = append(topics, []common.Hash{SwapoutTopic}) // SwapoutTopic or BTCSwapoutTopic
	topics = append(topics, []common.Hash{SwapoutTopic, BTCSwapoutTopic}) // SwapoutTopic or BTCSwapoutTopic

	for _, item := range config.ContractAddresses {
		pairID := strings.Split(item, ",")[0]
		addr := common.HexToAddress(strings.Split(item, ",")[1])
		server := strings.Split(item, ",")[2]

		addressesMap[addr] = pairID
		serverMap[addr] = server
		addresses = append(addresses, addr)
	}

	fq := ethereum.FilterQuery{
		Addresses: addresses,
		Topics:    topics,
	}

	log.Info("fq", "fq", fq)

	ctx := context.Background()
	client, err := ethclient.DialContext(ctx, endpoint)
	if err != nil {
		panic(err)
	}

	ch := make(chan types.Log, 128)
	defer close(ch)

	sub := LoopSubscribe(client, ctx, fq, ch)
	defer sub.Unsubscribe()

	go func() {
		for {
			select {
			case msg := <-ch:
				log.Info("Find event", "event", msg)
				txhash := msg.TxHash.String()
				pairID := addressesMap[msg.Address]
				fmt.Printf("txhash: %v, pairID: %v\n", txhash, pairID)
				server := serverMap[msg.Address]
				swaperr := DoSwapout(txhash, pairID, server)
				if swaperr != nil {
					log.Warn("Do swapout error", "error", swaperr)
				}
			case err := <-sub.Err():
				log.Info("Subscribe error", "error", err)
				sub.Unsubscribe()
				sub = LoopSubscribe(client, ctx, fq, ch)
			}
		}
	} ()

	headCh := make(chan *types.Header, 128)
	defer close(headCh)

	subHead := LoopSubscribeHead(client, ctx, headCh)
	defer subHead.Unsubscribe()

	go func() {
		for {
			select {
			case msg := <-headCh:
				log.Trace("Get new header", "head", msg.Number)
			case err := <-subHead.Err():
				log.Info("Subscribe error", "error", err)
				sub.Unsubscribe()
				sub = LoopSubscribe(client, ctx, fq, ch)
			}
		}
	} ()

	select{}
}

func LoopSubscribeHead(client *ethclient.Client, ctx context.Context, ch chan<- *types.Header) (ethereum.Subscription) {
	for {
		sub, err := client.SubscribeNewHead(ctx, ch)
		if err == nil {
			return sub
		}
		log.Info("Subscribe failed, retry in 1 second", "error", err)
		time.Sleep(time.Second * 1)
	}
}

func LoopSubscribe(client *ethclient.Client, ctx context.Context, fq ethereum.FilterQuery, ch chan types.Log) (ethereum.Subscription) {
	for {
		sub, err := client.SubscribeFilterLogs(ctx, fq, ch)
		if err == nil {
			log.Info("Subscribe start")
			return sub
		}
		log.Info("Subscribe failed, retry in 1 second", "error", err)
		time.Sleep(time.Second * 1)
	}
}

func DoSwapout(txid, pairID string, server string) error {
	client := &http.Client{}
	var data = strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","method":"swap.Swapout","params":[{"txid":"%v","pairid":"%v"}],"id":1}`, txid, pairID))
	req, err := http.NewRequest("POST", server, data)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	bodyText, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	log.Info("Call swap server", "response", fmt.Sprintf("%s", bodyText))
	return nil
}