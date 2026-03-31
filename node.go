package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

type PeerProvider struct {
	Info     peer.AddrInfo
	LastSeen time.Time
}

// Node represents the local p2pfs daemon
type Node struct {
	ctx            context.Context // ctx and cancel are used to manage the lifecycle of daemons.
	cancel         context.CancelFunc
	Host           host.Host                           // core engine provided by libp2p, representing your presence on the network.
	ExportDir      string                              // local path to the folder where shared files live.
	RpcSocket      string                              // path to the local Unix Domain Socket used for CLI commands.
	Providers      map[string]map[peer.ID]PeerProvider // filename -> Peer ID -> PeerProvider Info.
	providersLock  sync.RWMutex                        // prevents race conditions when accessing the Providers map. maps are not thread-safe in Go.
	LocalFiles     map[string]int64                    // cache of files currently in your ExportDir, mapping filename to size in bytes.
	localFilesLock sync.RWMutex                        // prevents race conditions when accessing the LocalFiles map.
	PubSub         *pubsub.PubSub                      // GossipSub for announcing and discovering files.
	rpcListener    net.Listener                        // rpcListener holds the open Unix Domain Socket listener for CLI clients.
}

// NewNode initializes a new libp2p node, connects to bootstrap nodes, and starts background tasks
func NewNode(listenAddr, exportDir, rpcSocket string, bootstrapAddrs []string) (*Node, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// 1. Create libp2p Host
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(listenAddr),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create host: %w", err)
	}

	n := &Node{
		ctx:        ctx,
		cancel:     cancel,
		Host:       h,
		ExportDir:  exportDir,
		RpcSocket:  rpcSocket,
		Providers:  make(map[string]map[peer.ID]PeerProvider),
		LocalFiles: make(map[string]int64),
	}

	log.Printf("Host created. Our Peer ID: %s", h.ID().String())
	for _, addr := range h.Addrs() {
		log.Printf("Listening on: %s/p2p/%s", addr, h.ID())
	}

	// 2. Setup PubSub
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		h.Close()
		cancel()
		return nil, fmt.Errorf("failed to create GossipSub: %w", err)
	}
	// ps is the PubSub instance that enables each node to join, publish to,
	// and subscribe to the topic
	// the actual subscription happens in discovery.go
	n.PubSub = ps

	// 3. Connect to bootstrap peers
	n.connectBootstrappers(bootstrapAddrs)

	// 4. Register RPC and background tasks
	// RPC server is the endpoint nodes expose
	// to accept CLI-issued commands
	if err := n.startRPCServer(); err != nil {
		h.Close()
		cancel()
		return nil, err
	}

	// Start scanning local directory periodically
	go n.scanLocalFiles()

	// Start Discovery/Announcements
	if err := n.setupDiscovery(); err != nil {
		log.Printf("Warning: setupDiscovery failed, pubsub may not work: %v", err)
	}

	// Register protocols
	n.setupTransferProtocol()
	n.setupIndexProtocol()

	return n, nil
}

func (n *Node) Close() error {
	n.cancel()
	if n.rpcListener != nil {
		n.rpcListener.Close()
	}
	return n.Host.Close()
}

// parses multiaddrs of bootstrap nodes and connects to them
func (n *Node) connectBootstrappers(addrs []string) {
	var wg sync.WaitGroup
	// iterate list of known bootstrap nodes and try to connect to ALL of them
	for _, addrStr := range addrs {
		addrStr := addrStr // capture loop vars
		if addrStr == "" {
			continue
		}

		// take IP and convert to protocol-agnostic multiaddr format
		maddr, err := multiaddr.NewMultiaddr(addrStr)
		if err != nil {
			log.Printf("Invalid bootstrap address %s: %v", addrStr, err)
			continue
		}

		info, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			log.Printf("Invalid bootstrap info %s: %v", addrStr, err)
			continue
		}

		wg.Add(1)

		// this part (the go routine) is non-blocking, so that one failed attempt
		// does not stall. so we will attempt to connect to all bootstrap nodes.
		go func(info peer.AddrInfo) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if err := n.Host.Connect(ctx, info); err != nil {
				log.Printf("Could not connect to bootstrap peer %s: %v", info.ID, err)
			} else {
				log.Printf("Connected to bootstrap peer %s", info.ID)
			}
		}(*info)
	}
	wg.Wait()
}

// wrapper to call updateLocalFiles periodically
func (n *Node) scanLocalFiles() {
	// we poll because we want to check whether the user has uploaded a new file in export_dir
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Run once immediately
	n.updateLocalFiles()

	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			n.updateLocalFiles()
			// Also GC providers
			n.evictExpiredProviders()
		}
	}
}

func (n *Node) updateLocalFiles() {
	files, err := os.ReadDir(n.ExportDir)
	if err != nil {
		log.Printf("Error reading export dir: %v", err)
		return
	}

	newFiles := make(map[string]int64)
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		newFiles[f.Name()] = info.Size()
	}

	n.localFilesLock.Lock()
	n.LocalFiles = newFiles
	n.localFilesLock.Unlock()
}

func (n *Node) evictExpiredProviders() {
	now := time.Now()
	ttl := 120 * time.Second // 2 min TTL

	n.providersLock.Lock()
	defer n.providersLock.Unlock()

	for file, peers := range n.Providers {
		for pid, pprovider := range peers {
			if now.Sub(pprovider.LastSeen) > ttl {
				delete(peers, pid)
			}
		}
		if len(peers) == 0 {
			delete(n.Providers, file)
		}
	}
}
