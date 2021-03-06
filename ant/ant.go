package ant

import (
	"errors"
	"log"
	"net"
	"os/exec"

	"gitlab.com/NebulousLabs/Sia/types"
	"github.com/NebulousLabs/go-upnp"
)

// AntConfig represents a configuration object passed to New(), used to
// configure a newly created Sia Ant.
type AntConfig struct {
	APIAddr         string `json:",omitempty"`
	RPCAddr         string `json:",omitempty"`
	HostAddr        string `json:",omitempty"`
	SiaDirectory    string `json:",omitempty"`
	Name            string `json:",omitempty"`
	SiadPath        string
	Jobs            []string
	DesiredCurrency uint64
}

// An Ant is a Sia Client programmed with network user stories. It executes
// these user stories and reports on their successfulness.
type Ant struct {
	APIAddr string
	RPCAddr string

	Config AntConfig

	siad *exec.Cmd
	jr   *jobRunner

	// A variable to track which blocks + heights the sync detector has seen
	// for this ant. The map will just keep growing, but it shouldn't take up a
	// prohibitive amount of space.
	SeenBlocks map[types.BlockHeight]types.BlockID `json:"-"`
}

// clearPorts discovers the UPNP enabled router and clears the ports used by an
// ant before the ant is started.
func clearPorts(config AntConfig) error {
	rpcaddr, err := net.ResolveTCPAddr("tcp", config.RPCAddr)
	if err != nil {
		return err
	}

	hostaddr, err := net.ResolveTCPAddr("tcp", config.HostAddr)
	if err != nil {
		return err
	}

	upnprouter, err := upnp.Discover()
	if err != nil {
		return err
	}

	err = upnprouter.Clear(uint16(rpcaddr.Port))
	if err != nil {
		return err
	}

	err = upnprouter.Clear(uint16(hostaddr.Port))
	if err != nil {
		return err
	}

	return nil
}

// New creates a new Ant using the configuration passed through `config`.
func New(config AntConfig) (*Ant, error) {
	var err error
	// unforward the ports required for this ant
	err = clearPorts(config)
	if err != nil {
		log.Printf("error clearing upnp ports for ant: %v\n", err)
	}

	// Construct the ant's Siad instance
	siad, err := newSiad(config.SiadPath, config.SiaDirectory, config.APIAddr, config.RPCAddr, config.HostAddr)
	if err != nil {
		return nil, err
	}

	// Ensure siad is always stopped if an error is returned.
	defer func() {
		if err != nil {
			stopSiad(config.APIAddr, siad.Process)
		}
	}()

	j, err := newJobRunner(config.APIAddr, "", config.SiaDirectory)
	if err != nil {
		return nil, err
	}

	for _, job := range config.Jobs {
		switch job {
		case "miner":
			go j.blockMining()
		case "host":
			go j.jobHost()
		case "renter":
			go j.storageRenter()
		case "gateway":
			go j.gatewayConnectability()
		}
	}

	if config.DesiredCurrency != 0 {
		go j.balanceMaintainer(types.SiacoinPrecision.Mul64(config.DesiredCurrency))
	}

	return &Ant{
		APIAddr: config.APIAddr,
		RPCAddr: config.RPCAddr,
		Config:  config,

		siad: siad,
		jr:   j,

		SeenBlocks: make(map[types.BlockHeight]types.BlockID),
	}, nil
}

// Close releases all resources created by the ant, including the Siad
// subprocess.
func (a *Ant) Close() error {
	a.jr.Stop()
	stopSiad(a.APIAddr, a.siad.Process)
	return nil
}

// StartJob starts the job indicated by `job` after an ant has been
// initialized. Arguments are passed to the job using args.
func (a *Ant) StartJob(job string, args ...interface{}) error {
	if a.jr == nil {
		return errors.New("ant is not running")
	}

	switch job {
	case "miner":
		go a.jr.blockMining()
	case "host":
		go a.jr.jobHost()
	case "renter":
		go a.jr.storageRenter()
	case "gateway":
		go a.jr.gatewayConnectability()
	case "bigspender":
		go a.jr.bigSpender()
	case "littlesupplier":
		go a.jr.littleSupplier(args[0].(types.UnlockHash))
	default:
		return errors.New("no such job")
	}

	return nil
}

// BlockHeight returns the highest block height seen by the ant.
func (a *Ant) BlockHeight() types.BlockHeight {
	height := types.BlockHeight(0)
	for h := range a.SeenBlocks {
		if h > height {
			height = h
		}
	}
	return height
}

// WalletAddress returns a wallet address that this ant can receive coins on.
func (a *Ant) WalletAddress() (*types.UnlockHash, error) {
	if a.jr == nil {
		return nil, errors.New("ant is not running")
	}

	addressGet, err := a.jr.client.WalletAddressGet()
	if err != nil {
		return nil, err
	}

	return &addressGet.Address, nil
}
