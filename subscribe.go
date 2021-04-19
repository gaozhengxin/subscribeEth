package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"time"
	"strings"
	"io/ioutil"

	"github.com/BurntSushi/toml"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
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

var SwapoutTopic common.Hash = common.HexToHash("0x628d6cba")
var BTCSwapoutTopic common.Hash = common.HexToHash("0xad54056d")

func init() {
	flag.StringVar(&configFile, "config", "./config.toml", "config")
}

func LoadConfig() *Config {
	config := &Config{}
	if _, err := toml.DecodeFile(configFile, &config); err != nil {
		panic(err)
	}
	return config
}

func main() {
	flag.Parse()

	config := LoadConfig()

	// 合约地址
	var addressesMap = make(map[common.Address]string)
	var addresses = make([]common.Address, 0)

	var endpoint string =  config.Endpoint

	topics := make([][]common.Hash, 0)
	for _, item := range config.ContractAddresses {
		pairID := strings.Split(item, ":")[0]
		addr := common.HexToAddress(strings.Split(item, ":")[1])

		addressesMap[addr] = pairID
		addresses = append(addresses, addr)
		topics = append(topics, []common.Hash{SwapoutTopic})
		topics = append(topics, []common.Hash{BTCSwapoutTopic})
	}

	fq := ethereum.FilterQuery{
		Addresses: addresses,
		Topics:    topics,
	}

	ctx := context.Background()
	client, err := ethclient.DialContext(ctx, endpoint)
	if err != nil {
		panic(err)
	}

	ch := make(chan types.Log, 128)
	defer close(ch)

	sub := LoopSubscribe(client, ctx, fq, ch)
	defer sub.Unsubscribe()

	for {
		select {
		case msg := <-ch:
			fmt.Printf("msg: %v\n", msg)
			txhash := msg.TxHash.String()
			pairID := addressesMap[msg.Address]
			swaperr := DoSwapout(txhash, pairID, config.Server)
			if swaperr != nil {
				fmt.Printf("Do swapout error: %v\n", swaperr)
			}
		case err := <-sub.Err():
			fmt.Printf("Subscribe error: %v", err)
			sub.Unsubscribe()
			sub = LoopSubscribe(client, ctx, fq, ch)
		}
	}
}

func LoopSubscribe(client *ethclient.Client, ctx context.Context, fq ethereum.FilterQuery, ch chan types.Log) (ethereum.Subscription) {
	for {
		sub, err := client.SubscribeFilterLogs(ctx, fq, ch)
		if err == nil {
			return sub
		}
		time.Sleep(time.Second * 1)
	}
}

func DoSwapout(txid, pairID string, server string) error {
	client := &http.Client{}
	var data = strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","method":"swap.Swapout","params":[{"txid":%v,"pairid":%v}],"id":1}`, txid, pairID))
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
	fmt.Printf("%s\n", bodyText)
	return nil
}