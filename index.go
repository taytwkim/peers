package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

/*
 * Index Protocol, stream-based request protocol allowing peers to manually verify what files a target peer is serving.
 *
 * setupIndexProtocol is called once in node startup.
 * doList issues the list request, and handleIndexStream is the handler.
 */

const indexProtocol = "/p2pfs/index/1.0.0"

type IndexRequest struct {
	Op       string `json:"op"` // "LIST" or "HAS"
	Filename string `json:"filename,omitempty"`
}

type IndexResponse struct {
	Files []string `json:"files,omitempty"`
	Has   bool     `json:"has,omitempty"`
	Error string   `json:"error,omitempty"`
}

func (n *Node) setupIndexProtocol() {
	n.Host.SetStreamHandler(indexProtocol, n.handleIndexStream)
}

func (n *Node) doList(targetAddr string) ([]string, error) {
	maddr, err := multiaddr.NewMultiaddr(targetAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid multiaddr: %w", err)
	}

	info, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return nil, fmt.Errorf("invalid addr info: %w", err)
	}

	ctx := context.Background()

	// Connect first if not already connected
	if err := n.Host.Connect(ctx, *info); err != nil {
		log.Printf("Warning: failed to connect to %s explicitly: %v", info.ID, err)
	}

	s, err := n.Host.NewStream(ctx, info.ID, indexProtocol)
	if err != nil {
		return nil, fmt.Errorf("failed to open index stream: %w", err)
	}
	defer s.Close()

	req := IndexRequest{Op: "LIST"}
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return nil, fmt.Errorf("failed to send LIST request: %w", err)
	}

	var resp IndexResponse
	if err := json.NewDecoder(s).Decode(&resp); err != nil {
		return nil, fmt.Errorf("failed to read LIST response: %w", err)
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("remote error: %s", resp.Error)
	}

	return resp.Files, nil
}

func (n *Node) handleIndexStream(s network.Stream) {
	defer s.Close()

	var req IndexRequest
	decoder := json.NewDecoder(s)
	if err := decoder.Decode(&req); err != nil {
		log.Printf("Failed to read index request: %v", err)
		return
	}

	encoder := json.NewEncoder(s)

	switch req.Op {
	case "LIST":
		log.Printf("Received LIST request from %s", s.Conn().RemotePeer())
		n.localFilesLock.RLock()
		var files []string
		for f := range n.LocalFiles {
			files = append(files, f)
		}
		n.localFilesLock.RUnlock()

		encoder.Encode(IndexResponse{Files: files})

	case "HAS":
		n.localFilesLock.RLock()
		_, exists := n.LocalFiles[req.Filename]
		n.localFilesLock.RUnlock()

		encoder.Encode(IndexResponse{Has: exists})

	default:
		encoder.Encode(IndexResponse{Error: "Unknown operation"})
	}
}
