package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

/*
 * Transfer Protocol, stream-based protocol handling file downloads.
 *
 * setupTransferProtocol is called once in node startup.
 * doFetch issues the transfer request, and handleTransferStream is the handler.
 */

const transferProtocol = "/p2pfs/get/1.0.0"

type TransferRequest struct {
	Filename string `json:"filename"`
}

type TransferResponse struct {
	Error    string `json:"error,omitempty"`
	Filesize int64  `json:"filesize,omitempty"`
}

func (n *Node) setupTransferProtocol() {
	n.Host.SetStreamHandler(transferProtocol, n.handleTransferStream)
}

func (n *Node) doFetch(filename string, targetPeerID string) error {
	var target peer.ID
	var err error
	var providerInfo peer.AddrInfo

	if targetPeerID != "" {
		target, err = peer.Decode(targetPeerID)
		if err != nil {
			return fmt.Errorf("invalid peer id: %v", err)
		}
	} else {
		// Find a peer from our providers index
		n.providersLock.RLock()
		peers, ok := n.Providers[filename]
		n.providersLock.RUnlock()

		if !ok || len(peers) == 0 {
			return errors.New("no providers known for this file. Use 'whohas' first")
		}

		// Just pick the first one for MVP
		for p, prov := range peers {
			target = p
			providerInfo = prov.Info
			break
		}
	}

	log.Printf("Fetching %s from %s", filename, target)

	ctx := context.Background() // For transfer, we just use background, but real app might want timeout

	if providerInfo.ID != "" && len(providerInfo.Addrs) > 0 {
		if err := n.Host.Connect(ctx, providerInfo); err != nil {
			log.Printf("Warning: failed to explicitly connect to provider: %v", err)
		}
	}

	s, err := n.Host.NewStream(ctx, target, transferProtocol)
	if err != nil {
		return fmt.Errorf("failed to open transfer stream: %w", err)
	}
	defer s.Close()

	// Send request
	req := TransferRequest{Filename: filename}
	encoder := json.NewEncoder(s)
	if err := encoder.Encode(req); err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}

	// Read response header
	var resp TransferResponse
	decoder := json.NewDecoder(s)
	if err := decoder.Decode(&resp); err != nil {
		return fmt.Errorf("failed to read response header: %w", err)
	}

	if resp.Error != "" {
		return fmt.Errorf("remote error: %s", resp.Error)
	}

	log.Printf("Incoming filesize: %d bytes", resp.Filesize)

	// Save to temp file
	tempPath := filepath.Join(n.ExportDir, filename+".downloading")
	finalPath := filepath.Join(n.ExportDir, filename)

	outFile, err := os.Create(tempPath)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	// Read data
	written, err := io.Copy(outFile, io.LimitReader(s, resp.Filesize))
	outFile.Close()

	if err != nil && err != io.EOF {
		os.Remove(tempPath)
		return fmt.Errorf("transfer failed mid-stream: %w", err)
	}

	if written != resp.Filesize {
		os.Remove(tempPath)
		return fmt.Errorf("incomplete file transfer: got %d, expected %d", written, resp.Filesize)
	}

	// Rename final
	if err := os.Rename(tempPath, finalPath); err != nil {
		return fmt.Errorf("failed to rename finalized file: %w", err)
	}

	// Update local files map so we instantly serve it
	n.localFilesLock.Lock()
	n.LocalFiles[filename] = resp.Filesize
	n.localFilesLock.Unlock()

	log.Printf("Successfully downloaded %s", filename)
	return nil
}

func (n *Node) handleTransferStream(s network.Stream) {
	defer s.Close()

	// Read Request
	var req TransferRequest
	decoder := json.NewDecoder(s)
	if err := decoder.Decode(&req); err != nil {
		log.Printf("Failed to read transfer request: %v", err)
		return
	}

	encoder := json.NewEncoder(s)

	log.Printf("Received GET request for %s from %s", req.Filename, s.Conn().RemotePeer())

	// Safety: reject path traversal or absolute paths
	if strings.Contains(req.Filename, "/") || strings.Contains(req.Filename, "\\") {
		encoder.Encode(TransferResponse{Error: "Invalid filename format"})
		return
	}

	targetPath := filepath.Join(n.ExportDir, req.Filename)

	// Check if file exists
	n.localFilesLock.RLock()
	size, exists := n.LocalFiles[req.Filename]
	n.localFilesLock.RUnlock()

	if !exists {
		encoder.Encode(TransferResponse{Error: "File not found"})
		return
	}

	file, err := os.Open(targetPath)
	if err != nil {
		encoder.Encode(TransferResponse{Error: "Internal server error"})
		return
	}
	defer file.Close()

	// Send Response Header
	if err := encoder.Encode(TransferResponse{Filesize: size}); err != nil {
		return
	}

	// Stream Bytes
	written, err := io.Copy(s, file)
	if err != nil {
		log.Printf("Error sending file %s: %v", req.Filename, err)
	} else {
		log.Printf("Sent %d bytes of %s to %s", written, req.Filename, s.Conn().RemotePeer())
	}
}
