package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"
	//"math/big"

	"github.com/BurntSushi/toml"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
)

type Config struct {
	Endpoint      string
	Server        string
	SwapoutTokens []string
	SwapinTokens  []string
}

var s struct {
	FOO struct {
		Usernames_Passwords map[string]string
	}
}

var configFile string

var (
	SwapoutTopic       common.Hash = common.HexToHash("0x6b616089d04950dc06c45c6dd787d657980543f89651aec47924752c7d16c888")
	BTCSwapoutTopic    common.Hash = common.HexToHash("0x9c92ad817e5474d30a4378deface765150479363a897b0590fbb12ae9d89396b")
	ERC20TransferTopic common.Hash = common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
)

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

	go StartSubscribeHeader(config)
	go StartSubscribeSwapout(config)
	go StartSubscribeSwapin(config)

	select {}
}

func StartSubscribeHeader(config *Config) {
	var endpoint string = config.Endpoint

	ctx := context.Background()
	client, err := ethclient.DialContext(ctx, endpoint)
	if err != nil {
		panic(err)
	}

	headCh := make(chan *types.Header, 128)
	defer close(headCh)

	sub := LoopSubscribeHead(client, ctx, headCh)
	defer sub.Unsubscribe()

	for {
		select {
		case msg := <-headCh:
			log.Trace("Get new header", "head", msg.Number)
		case err := <-sub.Err():
			log.Info("Subscribe error", "error", err)
			sub.Unsubscribe()
			sub = LoopSubscribeHead(client, ctx, headCh)
		}
	}
}

func StartSubscribeSwapout(config *Config) {
	var endpoint string = config.Endpoint

	ctx := context.Background()
	client, err := ethclient.DialContext(ctx, endpoint)
	if err != nil {
		panic(err)
	}

	var addreeePairIDMap = make(map[common.Address]string)
	var serverMap = make(map[common.Address]string)
	var tokenAddresses = make([]common.Address, 0)

	topics := make([][]common.Hash, 0)
	topics = append(topics, []common.Hash{SwapoutTopic, BTCSwapoutTopic}) // SwapoutTopic or BTCSwapoutTopic

	for _, item := range config.SwapoutTokens {
		pairID := strings.Split(item, ",")[0]
		addr := common.HexToAddress(strings.Split(item, ",")[1])
		server := strings.Split(item, ",")[2]

		addreeePairIDMap[addr] = pairID
		serverMap[addr] = server
		tokenAddresses = append(tokenAddresses, addr)
	}

	swapoutfq := ethereum.FilterQuery{
		Addresses: tokenAddresses,
		Topics:    topics,
	}

	log.Info("swapout fq", "swapoutfq", swapoutfq)

	ch := make(chan types.Log, 128)
	defer close(ch)

	sub := LoopSubscribe(client, ctx, swapoutfq, ch)
	defer sub.Unsubscribe()

	// subscribe swapout
	for {
		select {
		case msg := <-ch:
			log.Info("Find event", "event", msg)
			txhash := msg.TxHash.String()
			pairID := addreeePairIDMap[msg.Address]
			fmt.Printf("txhash: %v, pairID: %v\n", txhash, pairID)
			server := serverMap[msg.Address]
			swaperr := DoSwapout(txhash, pairID, server)
			if swaperr != nil {
				log.Warn("Do swapout error", "error", swaperr)
			}
		case err := <-sub.Err():
			log.Info("Subscribe error", "error", err)
			sub.Unsubscribe()
			sub = LoopSubscribe(client, ctx, swapoutfq, ch)
		}
	}
}

func StartSubscribeSwapin(config *Config) {
	var endpoint string = config.Endpoint

	ctx := context.Background()
	client, err := ethclient.DialContext(ctx, endpoint)
	if err != nil {
		panic(err)
	}

	var addreeePairIDMap = make(map[common.Address]string)
	var serverMap = make(map[common.Address]string)
	var depositAddressMap = make(map[common.Address][]common.Address)
	var ETHDepositAddress = common.Address{}

	for _, item := range config.SwapinTokens {
		pairID := strings.Split(item, ",")[0]
		tokenAddr := common.HexToAddress(strings.Split(item, ",")[1])
		depositAddr := common.HexToAddress(strings.Split(item, ",")[2])
		server := strings.Split(item, ",")[3]
	
		if tokenAddr == (common.Address{}) {
			ETHDepositAddress = depositAddr
		}

		addreeePairIDMap[tokenAddr] = pairID
		serverMap[tokenAddr] = server
		if depositAddressMap[depositAddr] == nil {
			depositAddressMap[depositAddr] = make([]common.Address, 1)
		}
		depositAddressMap[depositAddr] = append(depositAddressMap[depositAddr], tokenAddr)
	}

	// subscribe ERC20 swapin
	for depositAddr, tokens := range depositAddressMap {
		topics := make([][]common.Hash, 0)
		if depositAddr != ETHDepositAddress {
			topics = append(topics, []common.Hash{ERC20TransferTopic})       // Log [0] is ERC20 transfer
			topics = append(topics, []common.Hash{})                         // Log [1] is arbitrary
			topics = append(topics, []common.Hash{depositAddr.Hash()}) // Log [2] is deposit address
		} else {
			tokens = append(tokens, depositAddr)
		}

		fq := ethereum.FilterQuery{
			Addresses: tokens,
			Topics:    topics,
		}
		log.Info("swapin fq", "depositAddr", fq)

		go func() {
			ch := make(chan types.Log, 128)
			defer close(ch)

			sub := LoopSubscribe(client, ctx, fq, ch)
			defer sub.Unsubscribe()

			for {
				select {
				case msg := <-ch:
					log.Info("Find event", "event", msg)
					txhash := msg.TxHash.String()
					pairID := addreeePairIDMap[msg.Address]
					fmt.Printf("txhash: %v, pairID: %v\n", txhash, pairID)
					server := serverMap[msg.Address]
					swaperr := DoSwapin(txhash, pairID, server)
					if swaperr != nil {
						log.Warn("Do swapout error", "error", swaperr)
					}
				case err := <-sub.Err():
					log.Info("Subscribe error", "error", err)
					sub.Unsubscribe()
					sub = LoopSubscribe(client, ctx, fq, ch)
				}
			}
		}()
	}
}

func LoopSubscribeHead(client *ethclient.Client, ctx context.Context, ch chan<- *types.Header) ethereum.Subscription {
	for {
		sub, err := client.SubscribeNewHead(ctx, ch)
		if err == nil {
			return sub
		}
		log.Info("Subscribe failed, retry in 1 second", "error", err)
		time.Sleep(time.Second * 1)
	}
}

func LoopSubscribe(client *ethclient.Client, ctx context.Context, fq ethereum.FilterQuery, ch chan types.Log) ethereum.Subscription {
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

func DoSwapin(txid, pairID string, server string) error {
	client := &http.Client{}
	var data = strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","method":"swap.Swapin","params":[{"txid":"%v","pairid":"%v"}],"id":2}`, txid, pairID))
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
