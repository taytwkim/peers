package main

import (
	"log"
	"net"
	"net/http"
	"net/rpc"
	"os"
)

/*
 * rpc.go defines the glue between CLI and the local daemon.
 *
 * The workflow is:
 * 1. A CLI command in main.go creates an RPC Client and issues a request.
 * 2. The local daemon receives that request through a daemon-side RPC handler.
 * 3. The handlers are wrappers that trigger logic such as doFetch(...) or doList(...).
 */

// ============================================================================
// Shared RPC Types
// These structs define the request/response payloads passed between the CLI
// process and the local daemon over the Unix RPC socket.
// ============================================================================

type WhohasArgs struct {
	Filename string
}

type WhohasReply struct {
	Peers []string
}

type FetchArgs struct {
	Filename string
	PeerID   string
}

type FetchReply struct {
	Success bool
}

type ListArgs struct {
	TargetAddr string
}

type ListReply struct {
	Files []string
}

// ============================================================================
// Client-Side RPC Wrappers
// These methods are called by the CLI in main.go. They send local control
// commands to the running daemon over the Unix RPC socket.
// ============================================================================

// Client is the CLI-side RPC wrapper for talking to the local daemon.
type Client struct {
	rpcClient *rpc.Client
}

func NewClient(rpcSocket string) *Client {
	c, err := rpc.DialHTTP("unix", rpcSocket)
	if err != nil {
		log.Fatalf("Daemon not running or could not connect: %v", err)
	}
	return &Client{rpcClient: c}
}

func (c *Client) Whohas(filename string) ([]string, error) {
	args := &WhohasArgs{Filename: filename}
	var reply WhohasReply
	err := c.rpcClient.Call("P2PFSAPI.Whohas", args, &reply)
	return reply.Peers, err
}

func (c *Client) Fetch(filename, peerID string) error {
	args := &FetchArgs{Filename: filename, PeerID: peerID}
	var reply FetchReply
	return c.rpcClient.Call("P2PFSAPI.Fetch", args, &reply)
}

func (c *Client) List(targetAddr string) ([]string, error) {
	args := &ListArgs{TargetAddr: targetAddr}
	var reply ListReply
	err := c.rpcClient.Call("P2PFSAPI.List", args, &reply)
	return reply.Files, err
}

// ============================================================================
// Daemon-Side RPC Handlers
// These methods run inside the local daemon. They receive local control
// commands from the CLI and calls actual logic like doFetch and doList.
// ============================================================================

// P2PFSAPI is the daemon-side RPC receiver.
// When the CLI sends a command like Fetch or List, the local daemon receives it
// here and then calls into the Node to do the real work.
type P2PFSAPI struct {
	node *Node
}

func (api *P2PFSAPI) Whohas(args *WhohasArgs, reply *WhohasReply) error {
	api.node.providersLock.RLock()
	defer api.node.providersLock.RUnlock()

	peers := api.node.Providers[args.Filename]
	for pid := range peers {
		reply.Peers = append(reply.Peers, pid.String())
	}
	return nil
}

func (api *P2PFSAPI) Fetch(args *FetchArgs, reply *FetchReply) error {
	err := api.node.doFetch(args.Filename, args.PeerID)
	if err == nil {
		reply.Success = true
	}
	return err
}

func (api *P2PFSAPI) List(args *ListArgs, reply *ListReply) error {
	files, err := api.node.doList(args.TargetAddr)
	if err == nil {
		reply.Files = files
	}
	return err
}

// ============================================================================
// RPC Server Startup
// This creates the local Unix-socket server that the CLI connects to.
// Each node calls startRPCServer() at startup
// ============================================================================

func (n *Node) startRPCServer() error {
	api := &P2PFSAPI{node: n}
	rpcServer := rpc.NewServer()
	rpcServer.Register(api)

	// Clean up old socket if it exists.
	os.Remove(n.RpcSocket)

	l, err := net.Listen("unix", n.RpcSocket)
	if err != nil {
		return err
	}
	n.rpcListener = l

	go func() {
		defer l.Close()
		http.Serve(l, rpcServer)
	}()

	log.Printf("RPC Server listening on %s", n.RpcSocket)
	return nil
}
