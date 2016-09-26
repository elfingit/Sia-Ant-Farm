package main

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/julienschmidt/httprouter"
)

type (
	// AntfarmConfig contains the fields to parse and use to create a sia-antfarm.
	AntfarmConfig struct {
		ListenAddress string
		AntConfigs    []AntConfig
		AutoConnect   bool

		// ExternalFarms is a slice of net addresses representing the API addresses
		// of other ant farms to connect to.
		ExternalFarms []string
	}

	// antFarm defines the 'antfarm' type. antFarm orchestrates a collection of
	// ants and provides an API server to interact with them.
	antFarm struct {
		apiListener net.Listener
		wg          sync.WaitGroup
		ants        []*Ant
		router      *httprouter.Router
	}

	antsGET struct {
		Ants []*Ant
	}
)

// createAntfarm creates a new antFarm given the supplied AntfarmConfig
func createAntfarm(config AntfarmConfig) (*antFarm, error) {
	// clear old antfarm data before creating an antfarm
	os.RemoveAll("./antfarm-data")

	farm := &antFarm{}

	// start up each ant process with its jobs
	ants, err := startAnts(config.AntConfigs...)
	if err != nil {
		return nil, err
	}
	farm.ants = ants
	farm.wg.Add(len(ants))
	go func() {
		for _, ant := range ants {
			ant.Wait()
			farm.wg.Done()
		}
	}()

	err = func() error {
		// if the AutoConnect flag is set, use connectAnts to bootstrap the network.
		if config.AutoConnect {
			if err = connectAnts(ants...); err != nil {
				return err
			}
		}
		// start up the api server listener
		farm.apiListener, err = net.Listen("tcp", config.ListenAddress)
		if err != nil {
			return err
		}
		return nil
	}()

	if err != nil {
		farm.Close()
		return nil, err
	}

	go farm.permanentSyncMonitor()

	// construct the router and serve the API.
	farm.router = httprouter.New()
	farm.router.GET("/ants", farm.getAnts)

	return farm, nil
}

// connectExternalAntfarm connects the current antfarm to an external antfarm,
// using the antfarm api at externalAddress.
func (af *antFarm) connectExternalAntfarm(externalAddress string) error {
	res, err := http.DefaultClient.Get("http://" + externalAddress + "/ants")
	if err != nil {
		return err
	}
	defer res.Body.Close()

	var ag antsGET
	err = json.NewDecoder(res.Body).Decode(&ag)
	if err != nil {
		return err
	}
	ants := append(af.ants, ag.Ants...)
	return connectAnts(ants...)
}

// ServeAPI serves the antFarm's http API.
func (af *antFarm) ServeAPI() error {
	af.wg.Add(1)
	defer af.wg.Done()
	http.Serve(af.apiListener, af.router)
	return nil
}

// permanentSyncMonitor checks that all ants in the antFarm are on the same
// blockchain.
func (af *antFarm) permanentSyncMonitor() {
	// Give 30 seconds for everything to start up.
	time.Sleep(time.Second * 30)

	// Every 20 seconds, list all consensus groups.
	for {
		time.Sleep(time.Second * 20)
		groups, err := antConsensusGroups(af.ants...)
		if err != nil {
			log.Println("error checking sync status of antfarm: ", err)
			continue
		}
		if len(groups) == 1 {
			log.Println("Ants are synchronized.")
		} else {
			log.Println("Ants split into multiple groups, displaying")
			for i, group := range groups {
				if i != 0 {
					log.Println()
				}
				log.Println("Group ", i+1)
				for _, ant := range group {
					log.Println(ant.APIAddr)
				}
			}
		}
	}
}

// getAnts is a http handler that returns the ants currently running on the
// antfarm.
func (af *antFarm) getAnts(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	err := json.NewEncoder(w).Encode(antsGET{Ants: af.ants})
	if err != nil {
		http.Error(w, "error encoding ants", 500)
	}
}

// Close signals all the ants to stop and waits for them to return.
func (af *antFarm) Close() error {
	af.apiListener.Close()
	for _, ant := range af.ants {
		ant.Process.Signal(os.Interrupt)
	}
	af.wg.Wait()
	return nil
}
