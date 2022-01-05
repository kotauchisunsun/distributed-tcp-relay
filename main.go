package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
	"github.com/libp2p/go-libp2p-core/routing"
	discovery "github.com/libp2p/go-libp2p-discovery"
	dht "github.com/libp2p/go-libp2p-kad-dht"

	"github.com/multiformats/go-multiaddr"
)

// These variables are set in build step
var (
	Version  = "unset"
	Revision = "unset"
)

func handleStream(serverAddr string,s network.Stream) {
	log.Println("Got a new stream!")
 
	tcpAddr, err := net.ResolveTCPAddr("tcp", serverAddr)
	if err != nil {
		log.Fatal(err)
	}

	log.Println("Connect To Tcp Socket")
    conn, err := net.Dial("tcp",tcpAddr.String())
    if err != nil {
		log.Fatal(err)
    }
	
	go func(){
		io.Copy(conn,s)
		log.Println("Stop Copy To Stream From Tcp Socket")
	}()

	go func(){
		io.Copy(s,conn)
		log.Println("Stop Copy To Tcp Socket From Stream")
	}()
}


func startServer(ctx context.Context,h host.Host,pipingClientAddr string,peerInitFunc func(context.Context,host.Host) error) {
	log.Println("Starting Server Mode")
	log.Printf("relay connect to pipingClientAddr: %s\n", pipingClientAddr)
	startPeer(ctx, h, func(s network.Stream){
		handleStream(pipingClientAddr,s)
	}, peerInitFunc)
}

func startClient(ctx context.Context,h host.Host,pipingServerAddr string, connectToFunc func(context.Context, host.Host) (peer.ID,error)) {
	log.Println("Starting Client Mode")
	log.Printf("relay listen pipingServerAddr: %s\n", pipingServerAddr)
	// tcpの接続アドレスを作成する
	tcpAddr, err := net.ResolveTCPAddr("tcp", pipingServerAddr)
	if err != nil {
		log.Fatal(err)
	}

	// リスナーを作成する
	listener, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		log.Fatal(err)
	}

	log.Println("This node's multiaddresses:")
	for _, la := range h.Addrs() {
		log.Printf(" - %v\n", la)
	}
	log.Println()

	peerID,err := connectToFunc(ctx,h)
	if err != nil {
		log.Println(err)
	}

	for {
		log.Println("Waiting for connection...")

		// クライアントからのコネクション情報を受け取る
		conn, err := listener.AcceptTCP() 
		defer conn.Close()
		if err != nil {
			log.Fatal(err)
		}

		s, err := startPeerStream(ctx, h, peerID)
		if err != nil {
			log.Println(err)
			return
		}

		go func(){
			io.Copy(conn,s)
			log.Println("Stop Copy To Stream From Tcp Socket")
		}()
	
		go func(){
			io.Copy(s,conn)
			log.Println("Stop Copy To Tcp Socket From Stream")
		}()
	}
}

func initPeerLocal(ctx context.Context, h host.Host) error {
	var port string
	for _, la := range h.Network().ListenAddresses() {
		if p, err := la.ValueForProtocol(multiaddr.P_TCP); err == nil {
			port = p
			break
		}
	}

	if port == "" {
		log.Println("was not able to find actual local port")
		return nil
	}

	log.Printf("go run relay.go -d /ip4/127.0.0.1/tcp/%v/p2p/%s\n", port, h.ID().Pretty())
	return nil
}

func initPeerByDHT(ctx context.Context, h host.Host, rendezvousString string) error {
	kademliaDHT, err := connectToBootstrapPeers(ctx,h)

	if err != nil {
		return err
	}

	log.Println("Announcing ourselves...")
	routingDiscovery := discovery.NewRoutingDiscovery(kademliaDHT)
	discovery.Advertise(ctx, routingDiscovery, rendezvousString)
	log.Println("Successfully announced!")

	return nil
}


func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourcePort := flag.Int("sp", 0, "Source port number")
	pipingServerAddr := flag.String("sa", "localhost:3000", "Server address")
	pipingClientAddr := flag.String("ca", "localhost:2000", "Client address")
	//dhtDest := flag.String("d", "", "Destination multiaddr string")
	rendezvous := flag.String("r", "", "Rendezvous name")
	isServer := flag.Bool("s",false,"Server Mode")

	flag.Parse()

	r := rand.Reader

	h, err := makeHost(ctx,*sourcePort, r)
	if err != nil {
		log.Println(err)
		return
	}

	if *isServer {
		//startServer(ctx,h,*pipingClientAddr,initPeerLocal)
		startServer(ctx,h,*pipingClientAddr, func(c context.Context, h host.Host) error {
			return initPeerByDHT(ctx,h,*rendezvous)
		})
	} else {
		/*
		startClient(ctx,h,*pipingServerAddr, func(ctx context.Context, host.Host) (peer.ID,error){
			return connectLocalDestination(host,*dhtDest)
		})*/
		startClient(ctx,h,*pipingServerAddr, func(ctx context.Context,h host.Host) (peer.ID,error){
			return connectByDHT(ctx,h, *rendezvous)
		})
	}

	select {}
}

func makeHost(ctx context.Context,port int, randomness io.Reader) (host.Host, error) {
	prvKey, _, err := crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, randomness)
	if err != nil {
		log.Println(err)
		return nil, err
	}

	sourceMultiAddr, _ := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port))

	return libp2p.New(
		libp2p.NATPortMap(),
		libp2p.ListenAddrs(sourceMultiAddr),
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			idht, err := connectToBootstrapPeers(ctx,h)
			return idht, err
		}),
		libp2p.EnableNATService(),
		libp2p.EnableAutoRelay(),
		libp2p.Identity(prvKey),
	)
}

func startPeer(ctx context.Context, h host.Host, streamHandler network.StreamHandler, peerInitFunc func(context.Context,host.Host) error) {
	h.SetStreamHandler("/chat/1.0.0", streamHandler)

	peerInitFunc(ctx,h)
}

func connectLocalDestination(h host.Host, destination string) (peer.ID,error) {
	maddr, err := multiaddr.NewMultiaddr(destination)
	if err != nil {
		log.Println(err)
		return "",err
	}

	info, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		log.Println(err)
		return "",err
	}

	h.Peerstore().AddAddrs(info.ID, info.Addrs, peerstore.PermanentAddrTTL)
	return info.ID,nil
}

func connectToBootstrapPeers(ctx context.Context, h host.Host) (*dht.IpfsDHT,error) {
	kademliaDHT, err := dht.New(ctx, h)
	if err != nil {
		return nil,err
	}

	log.Println("Bootstrapping the DHT")
	if err = kademliaDHT.Bootstrap(ctx); err != nil {
		return nil,err
	}

	var wg sync.WaitGroup
	for _, peerAddr := range dht.DefaultBootstrapPeers {
		peerinfo, _ := peer.AddrInfoFromP2pAddr(peerAddr)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.Connect(ctx, *peerinfo); err != nil {
				log.Println(err)
			} else {
				log.Println("Connection established with bootstrap node:", *peerinfo)
			}
		}()
	}
	wg.Wait()

	return kademliaDHT,nil
}


func connectByDHT(ctx context.Context,h host.Host, rendezvousString string) (peer.ID,error){
	kademliaDHT, err := connectToBootstrapPeers(ctx,h)

	if err != nil {
		return "",err
	}

	routingDiscovery := discovery.NewRoutingDiscovery(kademliaDHT)

	for i := 0; i < 10; i++ {
		peerChan, err := routingDiscovery.FindPeers(ctx, rendezvousString)
		if err != nil {
			panic(err)
		}
	
		for peer := range peerChan {
			if peer.ID == h.ID() {
				continue
			}

			log.Println("Found peer:", peer)
			return peer.ID,nil
		}

		log.Println("Retry Peer Search...")
		time.Sleep(100 * time.Millisecond)
	}

	return "",nil
}

func startPeerStream(ctx context.Context, h host.Host, peerID peer.ID) (network.Stream, error) {
	s, err := h.NewStream(context.Background(), peerID, "/chat/1.0.0")
	if err != nil {
		log.Println(err)
		return nil, err
	}
	log.Println("Established connection to destination")

	return s, nil
}
