package main

import (
	"github.com/jlatt/ergonomadic/irc"
	"log"
	"sync"
)

func main() {
	config, err := irc.LoadConfig()
	if err != nil {
		log.Fatal(err)
		return
	}

	irc.DEBUG_NET = config.Debug["net"]
	irc.DEBUG_CLIENT = config.Debug["client"]
	irc.DEBUG_CHANNEL = config.Debug["channel"]
	irc.DEBUG_SERVER = config.Debug["server"]

	irc.NewServer(config)

	// never finishes
	wg := sync.WaitGroup{}
	wg.Add(1)
	wg.Wait()
}