package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/multiformats/go-multiaddr"
	"github.com/rivo/tview"

	"p2p-messenger/internal/domain/chat"
	"p2p-messenger/internal/infrastructure/e2e"
	"p2p-messenger/internal/infrastructure/identity"
)

type App struct {
	host        host.Host
	ps          *pubsub.PubSub
	topics      map[string]*pubsub.Topic
	mu          sync.Mutex
	nickname    string
	curRoom     string
	rooms       []string
	roomMembers map[string]map[string]*chat.Member
	roomKeys    map[string][]byte
	torMode     bool

	tapp         *tview.Application
	chatView     *tview.TextView
	inputField   *tview.InputField
	sidebar      *tview.List
	statusBar    *tview.TextView
	peerCount    int
	resetPending bool
}

type mdnsNotifee struct {
	app *App
}

func (n *mdnsNotifee) HandlePeerFound(pi peer.AddrInfo) {
	go n.app.host.Connect(context.Background(), pi) //nolint
}

func newApp(ctx context.Context, nickname, torAddr string) (*App, error) {
	fmt.Print("Loading identity... ")
	priv, isNew, err := identity.LoadOrCreate()
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}

	if isNew {
		fmt.Printf("new identity created (%s)\n", identity.KeyPath())
	} else {
		fmt.Println("loaded existing identity")
	}

	options := []libp2p.Option{libp2p.Identity(priv)}
	if torAddr != "" {
		if err := os.Setenv("ALL_PROXY", "socks5://"+torAddr); err != nil {
			return nil, fmt.Errorf("set proxy env: %w", err)
		}

		options = append(options, libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
		fmt.Printf("Tor mode enabled (%s)\n", torAddr)
	}

	fmt.Print("Creating P2P host... ")
	h, err := libp2p.New(options...)
	if err != nil {
		return nil, fmt.Errorf("create host: %w", err)
	}
	fmt.Println("done")

	fmt.Print("Creating PubSub... ")
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return nil, fmt.Errorf("create pubsub: %w", err)
	}
	fmt.Println("done")

	app := &App{
		host:        h,
		ps:          ps,
		topics:      make(map[string]*pubsub.Topic),
		nickname:    nickname,
		curRoom:     chat.GlobalRoomName,
		rooms:       []string{chat.GlobalRoomName},
		roomMembers: make(map[string]map[string]*chat.Member),
		roomKeys:    make(map[string][]byte),
		torMode:     torAddr != "",
	}

	fmt.Print("Starting mDNS discovery... ")
	mdnsSvc := mdns.NewMdnsService(h, DiscoveryTag, &mdnsNotifee{app: app})
	if err := mdnsSvc.Start(); err != nil {
		return nil, fmt.Errorf("mdns: %w", err)
	}
	fmt.Println("done")

	go func() {
		time.Sleep(2 * time.Second)

		kadDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeAutoServer))
		if err != nil {
			debugLog("DHT creation error: %v", err)
			return
		}

		routingDiscovery := drouting.NewRoutingDiscovery(kadDHT)

		for _, addr := range bootstrapPeers {
			go func(addr string) {
				ma, err := multiaddr.NewMultiaddr(addr)
				if err != nil {
					return
				}

				pi, err := peer.AddrInfoFromP2pAddr(ma)
				if err != nil {
					return
				}

				connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				if err := h.Connect(connectCtx, *pi); err == nil {
					debugLog("Connected to bootstrap: %s", pi.ID.String()[:8])
				}
			}(addr)
		}

		if err := kadDHT.Bootstrap(ctx); err != nil {
			debugLog("DHT bootstrap warning: %v", err)
		}

		go dutil.Advertise(ctx, routingDiscovery, DiscoveryTag)
		debugLog("DHT initialized")

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				findCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				peerCh, err := routingDiscovery.FindPeers(findCtx, DiscoveryTag)
				cancel()
				if err != nil {
					continue
				}

				for pi := range peerCh {
					if pi.ID != h.ID() && len(pi.Addrs) > 0 {
						go func(pi peer.AddrInfo) {
							connectCtx, connectCancel := context.WithTimeout(ctx, 5*time.Second)
							defer connectCancel()
							h.Connect(connectCtx, pi) //nolint
						}(pi)
					}
				}
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if peers := h.Network().Peers(); len(peers) > 0 {
					debugLog("Connected to %d peers", len(peers))
				}
			}
		}
	}()

	h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, _ network.Conn) {
			app.mu.Lock()
			app.peerCount++
			app.mu.Unlock()
			app.updateStatus()
		},
		DisconnectedF: func(_ network.Network, _ network.Conn) {
			app.mu.Lock()
			if app.peerCount > 0 {
				app.peerCount--
			}
			app.mu.Unlock()
			app.updateStatus()
		},
	})

	return app, nil
}

func (a *App) ensureRoomMembersLocked(room string) map[string]*chat.Member {
	members := a.roomMembers[room]
	if members == nil {
		members = make(map[string]*chat.Member)
		a.roomMembers[room] = members
	}

	return members
}

func (a *App) joinedRoomsSnapshot() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	rooms := make([]string, len(a.rooms))
	copy(rooms, a.rooms)
	return rooms
}

func (a *App) joinRoom(room string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.topics[room]; ok {
		a.curRoom = room
		return nil
	}

	topic, err := a.ps.Join(room)
	if err != nil {
		return err
	}

	subscription, err := topic.Subscribe()
	if err != nil {
		return err
	}

	a.topics[room] = topic
	a.ensureRoomMembersLocked(room)

	found := false
	for _, existingRoom := range a.rooms {
		if existingRoom == room {
			found = true
			break
		}
	}

	if !found {
		a.rooms = append(a.rooms, room)
	}

	a.curRoom = room
	go a.readMessages(room, subscription)
	return nil
}

func (a *App) publishMsg(messageType chat.MessageType, room, text string) error {
	a.mu.Lock()
	topic, ok := a.topics[room]
	key := a.roomKeys[room]
	nickname := a.nickname
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("not in room %s", room)
	}

	message := chat.NewMessage(messageType, a.host.ID().String(), nickname, room, "", time.Now())
	if messageType == chat.MessageTypeChat && key != nil {
		encrypted, err := e2e.Encrypt([]byte(text), key)
		if err != nil {
			return fmt.Errorf("encrypt: %w", err)
		}

		message.Encrypted = encrypted
	} else {
		message.Text = text
	}

	body, err := json.Marshal(message)
	if err != nil {
		return err
	}

	return topic.Publish(context.Background(), body)
}

func (a *App) readMessages(room string, subscription *pubsub.Subscription) {
	for {
		envelope, err := subscription.Next(context.Background())
		if err != nil {
			return
		}

		if envelope.ReceivedFrom == a.host.ID() {
			continue
		}

		var message chat.Message
		if err := json.Unmarshal(envelope.Data, &message); err != nil {
			continue
		}

		if message.Room == "" {
			message.Room = room
		}

		now := time.Now()
		a.mu.Lock()
		members := a.ensureRoomMembersLocked(room)
		existing, seen := members[message.From]
		isNewcomer := !seen || !existing.IsOnline(now)
		if message.Type != chat.MessageTypeLeave {
			members[message.From] = chat.NewMember(message.Nickname, now)
		}
		a.mu.Unlock()

		switch message.Type {
		case chat.MessageTypeJoin:
			a.systemMsg(fmt.Sprintf("[green]-> %s[white] joined [cyan]%s", message.Nickname, room))
			a.updateStatus()
		case chat.MessageTypeLeave:
			a.systemMsg(fmt.Sprintf("[red]<- %s[white] left [cyan]%s", message.Nickname, room))
			a.mu.Lock()
			delete(a.roomMembers[room], message.From)
			a.mu.Unlock()
			a.updateStatus()
		case chat.MessageTypePing:
			if isNewcomer {
				a.systemMsg(fmt.Sprintf("[green]* %s[white] online in [cyan]%s", message.Nickname, room))
				a.updateStatus()
			}
		case chat.MessageTypeChat:
			if isNewcomer {
				a.systemMsg(fmt.Sprintf("[green]-> %s[white] appeared in [cyan]%s", message.Nickname, room))
				a.updateStatus()
			}

			if message.Encrypted != "" {
				a.mu.Lock()
				key := a.roomKeys[room]
				a.mu.Unlock()
				if key == nil {
					a.systemMsg(fmt.Sprintf("[red]%s[white] sent encrypted msg — use [green]/key %s <password>", message.Nickname, room))
					continue
				}

				plaintext, err := e2e.Decrypt(message.Encrypted, key)
				if err != nil {
					a.systemMsg(fmt.Sprintf("[red]Failed to decrypt msg from %s — wrong password?", message.Nickname))
					continue
				}

				message.Text = string(plaintext)
			}

			a.appendChat(message)
		}
	}
}

func (a *App) startPresence(ctx context.Context) {
	for _, room := range a.joinedRoomsSnapshot() {
		room := room
		go a.publishMsg(chat.MessageTypeJoin, room, "") //nolint
	}

	ticker := time.NewTicker(chat.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			for _, room := range a.joinedRoomsSnapshot() {
				room := room
				go a.publishMsg(chat.MessageTypeLeave, room, "") //nolint
			}
			return
		case <-ticker.C:
			for _, room := range a.joinedRoomsSnapshot() {
				room := room
				go a.publishMsg(chat.MessageTypePing, room, "") //nolint
			}
		}
	}
}
