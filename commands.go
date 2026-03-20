package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"p2p-messenger/internal/domain/chat"
	"p2p-messenger/internal/infrastructure/e2e"
	"p2p-messenger/internal/infrastructure/identity"
)

func (a *App) restoreInputTitle() {
	if a.tapp == nil || a.inputField == nil {
		return
	}

	a.tapp.QueueUpdateDraw(func() {
		a.inputField.SetBorderColor(tcell.ColorTeal).
			SetTitle(defaultInputTitle)
	})
}

func (a *App) cancelPendingReset() bool {
	a.mu.Lock()
	wasPending := a.resetPending
	a.resetPending = false
	a.mu.Unlock()

	if !wasPending {
		return false
	}

	a.restoreInputTitle()
	a.systemMsg("Reset cancelled.")
	return true
}

func (a *App) handleInput(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}

	if strings.HasPrefix(raw, "/") {
		a.handleCommand(raw)
		return
	}

	a.cancelPendingReset()

	a.mu.Lock()
	room := a.curRoom
	nickname := a.nickname
	a.mu.Unlock()

	message := chat.NewMessage(chat.MessageTypeChat, a.host.ID().String(), nickname, room, raw, time.Now())
	a.appendChat(message)

	go func() {
		if err := a.publishMsg(chat.MessageTypeChat, room, raw); err != nil {
			a.systemMsg("send error: " + err.Error())
		}
	}()
}

func (a *App) handleCommand(raw string) {
	parts := strings.Fields(raw)
	cmd := parts[0]

	if cmd != "/reset" && a.cancelPendingReset() && cmd != "/clear" {
		return
	}

	switch cmd {
	case "/help":
		a.tapp.QueueUpdateDraw(func() {
			fmt.Fprint(a.chatView, "\n[cyan]Commands:[white]\n")
			fmt.Fprint(a.chatView, "  [green]/who[white]              — who is online in current room\n")
			fmt.Fprint(a.chatView, "  [green]/join <room>[white]      — join or create a room\n")
			fmt.Fprint(a.chatView, "  [green]/switch <room>[white]    — switch active room\n")
			fmt.Fprint(a.chatView, "  [green]/rooms[white]            — list joined rooms\n")
			fmt.Fprint(a.chatView, "  [green]/peers[white]            — show direct P2P connections\n")
			fmt.Fprint(a.chatView, "  [green]/id[white]               — show your Peer ID and addresses\n")
			fmt.Fprint(a.chatView, "  [green]/nick <name>[white]      — change nickname\n")
			fmt.Fprint(a.chatView, "  [green]/connect <addr>[white]   — manually connect via multiaddr\n")
			fmt.Fprint(a.chatView, "  [green]/key <password>[white]   — enable E2E encryption in current room\n")
			fmt.Fprint(a.chatView, "  [green]/unkey[white]            — disable encryption in current room\n")
			fmt.Fprint(a.chatView, "  [green]/lock[white]             — show encryption status of rooms\n")
			fmt.Fprint(a.chatView, "  [green]/clear[white]            — clear chat screen\n")
			fmt.Fprint(a.chatView, "  [green]/reset[white]            — delete identity.key (confirm twice)\n")
			fmt.Fprint(a.chatView, "  [green]/quit[white]             — exit\n\n")
			a.chatView.ScrollToEnd()
		})

	case "/who":
		type entry struct {
			nick string
			ago  time.Duration
			pid  string
		}

		now := time.Now()
		var online []entry
		var offline []entry

		a.mu.Lock()
		room := a.curRoom
		for peerID, member := range a.roomMembers[room] {
			ago := now.Sub(member.LastSeen).Truncate(time.Second)
			item := entry{
				nick: member.Nickname,
				ago:  ago,
				pid:  shortID(peer.ID(peerID)),
			}
			if member.IsOnline(now) {
				online = append(online, item)
			} else {
				offline = append(offline, item)
			}
		}
		myNick := a.nickname
		a.mu.Unlock()

		sort.Slice(online, func(i, j int) bool { return online[i].nick < online[j].nick })
		sort.Slice(offline, func(i, j int) bool { return offline[i].nick < offline[j].nick })

		a.tapp.QueueUpdateDraw(func() {
			fmt.Fprintf(a.chatView, "\n[cyan]Members of room [cyan::b]%s[white]:\n", room)
			fmt.Fprintf(a.chatView, "  [green]*[white] [green::b]%s[white] [dimgray](you)[white]\n", myNick)
			for _, member := range online {
				fmt.Fprintf(a.chatView, "  [green]*[white] [green]%s[white]  [dimgray]%s  active %s ago[white]\n", member.nick, member.pid, member.ago)
			}
			if len(offline) > 0 {
				fmt.Fprint(a.chatView, "[dimgray]  -- possibly offline --[white]\n")
				for _, member := range offline {
					fmt.Fprintf(a.chatView, "  [red]o[white] [dimgray]%s  %s  %s ago[white]\n", member.nick, member.pid, member.ago)
				}
			}
			if len(online) == 0 && len(offline) == 0 {
				fmt.Fprint(a.chatView, "  [dimgray]No one else yet[white]\n")
			}
			fmt.Fprintln(a.chatView)
			a.chatView.ScrollToEnd()
		})

	case "/join":
		if len(parts) < 2 {
			a.systemMsg("usage: /join <room-name>")
			return
		}

		room := parts[1]
		if err := a.joinRoom(room); err != nil {
			a.systemMsg("join error: " + err.Error())
			return
		}

		go a.publishMsg(chat.MessageTypeJoin, room, "") //nolint
		a.systemMsg(fmt.Sprintf("Joined room [cyan]%s", room))
		a.updateStatus()

	case "/switch":
		if len(parts) < 2 {
			a.systemMsg("usage: /switch <room-name>")
			return
		}

		room := parts[1]
		a.mu.Lock()
		_, ok := a.topics[room]
		if ok {
			a.curRoom = room
		}
		a.mu.Unlock()
		if !ok {
			a.systemMsg(fmt.Sprintf("Not in [cyan]%s[white]. Use /join first.", room))
			return
		}

		a.systemMsg(fmt.Sprintf("Switched to [cyan]%s", room))
		a.updateStatus()

	case "/rooms":
		now := time.Now()
		a.mu.Lock()
		rooms := make([]string, len(a.rooms))
		copy(rooms, a.rooms)
		currentRoom := a.curRoom
		roomMembers := make(map[string]int, len(a.roomMembers))
		for roomName, members := range a.roomMembers {
			roomMembers[roomName] = chat.CountOnline(members, now)
		}
		a.mu.Unlock()

		a.tapp.QueueUpdateDraw(func() {
			fmt.Fprint(a.chatView, "\n[cyan]Your rooms:[white]\n")
			for _, roomName := range rooms {
				mark := "  "
				if roomName == currentRoom {
					mark = "[green]> "
				}
				fmt.Fprintf(a.chatView, "%s[white]%s  [dimgray](%d members)[white]\n", mark, roomName, roomMembers[roomName]+1)
			}
			fmt.Fprintln(a.chatView)
			a.chatView.ScrollToEnd()
		})

	case "/peers":
		type peerEntry struct {
			id   string
			nick string
		}

		peers := a.host.Network().Peers()
		entries := make([]peerEntry, 0, len(peers))

		a.mu.Lock()
		for _, remotePeer := range peers {
			nick := "unknown"
			for _, members := range a.roomMembers {
				if member, ok := members[remotePeer.String()]; ok {
					nick = member.Nickname
					break
				}
			}
			entries = append(entries, peerEntry{id: shortID(remotePeer), nick: nick})
		}
		a.mu.Unlock()

		a.tapp.QueueUpdateDraw(func() {
			if len(entries) == 0 {
				fmt.Fprint(a.chatView, "[yellow]  * No direct P2P connections yet[white]\n")
				a.chatView.ScrollToEnd()
				return
			}

			fmt.Fprintf(a.chatView, "\n[cyan]Direct P2P connections (%d):[white]\n", len(entries))
			for _, entry := range entries {
				fmt.Fprintf(a.chatView, "  [yellow]%s[white]  %s\n", entry.id, entry.nick)
			}
			fmt.Fprintln(a.chatView)
			a.chatView.ScrollToEnd()
		})

	case "/nick":
		if len(parts) < 2 {
			a.systemMsg("usage: /nick <new-name>")
			return
		}

		a.mu.Lock()
		a.nickname = parts[1]
		a.mu.Unlock()
		a.systemMsg(fmt.Sprintf("Nickname changed to [green]%s", parts[1]))

	case "/id":
		a.tapp.QueueUpdateDraw(func() {
			id := a.host.ID().String()
			addrs := a.host.Addrs()
			fmt.Fprint(a.chatView, "\n[teal]+--------------------------------------------------+[white]\n")
			fmt.Fprint(a.chatView, "[teal]|[white]              YOUR IDENTITY                    [teal]|[white]\n")
			fmt.Fprint(a.chatView, "[teal]+--------------------------------------------------+[white]\n")
			fmt.Fprintf(a.chatView, "[teal]|[yellow] %s\n", id)
			fmt.Fprint(a.chatView, "[teal]+--------------------------------------------------+[white]\n")
			fmt.Fprint(a.chatView, "[teal]|[white] Addresses for /connect:                      [teal]|[white]\n")
			for _, addr := range addrs {
				fmt.Fprintf(a.chatView, "[teal]|[cyan]  %s/p2p/%s\n", addr, id)
			}
			fmt.Fprintf(a.chatView, "[teal]+--------------------------------------------------+[white]\n")
			fmt.Fprintf(a.chatView, "[teal]|[dimgray] Key: %s\n", identity.KeyPath())
			fmt.Fprint(a.chatView, "[teal]+--------------------------------------------------+[white]\n\n")
			a.chatView.ScrollToEnd()
		})

	case "/key":
		if len(parts) < 2 {
			a.systemMsg("usage: /key <password>  or  /key <room> <password>")
			return
		}

		var room string
		var password string
		if len(parts) >= 3 {
			room = parts[1]
			password = strings.Join(parts[2:], " ")
		} else {
			a.mu.Lock()
			room = a.curRoom
			a.mu.Unlock()
			password = strings.Join(parts[1:], " ")
		}

		key := e2e.DeriveRoomKey(password)
		a.mu.Lock()
		a.roomKeys[room] = key
		a.mu.Unlock()
		a.updateStatus()

		a.tapp.QueueUpdateDraw(func() {
			fmt.Fprintf(a.chatView, "\n[green]E2E encryption enabled for [cyan]%s[white]\n", room)
			fmt.Fprint(a.chatView, "[dimgray]  Algorithm: XChaCha20-Poly1305\n")
			fmt.Fprint(a.chatView, "  Key derived from password via SHA-256\n")
			fmt.Fprint(a.chatView, "  Relay peers see only encrypted blob\n")
			fmt.Fprint(a.chatView, "  All room members must use the same password[white]\n\n")
			a.chatView.ScrollToEnd()
		})

	case "/unkey":
		a.mu.Lock()
		room := a.curRoom
		_, hadKey := a.roomKeys[room]
		delete(a.roomKeys, room)
		a.mu.Unlock()
		a.updateStatus()

		if hadKey {
			a.systemMsg(fmt.Sprintf("[yellow]Encryption removed from [cyan]%s[white].", room))
		} else {
			a.systemMsg(fmt.Sprintf("Room [cyan]%s[white] was not encrypted.", room))
		}

	case "/lock":
		a.mu.Lock()
		rooms := make([]string, len(a.rooms))
		copy(rooms, a.rooms)
		keys := make(map[string]bool, len(a.roomKeys))
		for roomName, key := range a.roomKeys {
			keys[roomName] = key != nil
		}
		a.mu.Unlock()

		a.tapp.QueueUpdateDraw(func() {
			fmt.Fprint(a.chatView, "\n[cyan]Encryption status:[white]\n")
			for _, roomName := range rooms {
				if keys[roomName] {
					fmt.Fprintf(a.chatView, "  [green][E2E] %s[white]  — encrypted\n", roomName)
				} else {
					fmt.Fprintf(a.chatView, "  [yellow][---] %s[white]  — plaintext\n", roomName)
				}
			}
			fmt.Fprintln(a.chatView)
			a.chatView.ScrollToEnd()
		})

	case "/clear":
		a.tapp.QueueUpdateDraw(func() {
			a.chatView.Clear()
			fmt.Fprint(a.chatView, "[dimgray]  -- screen cleared --[white]\n")
		})

	case "/reset":
		a.mu.Lock()
		pending := a.resetPending
		a.mu.Unlock()

		if !pending {
			a.mu.Lock()
			a.resetPending = true
			a.mu.Unlock()

			a.tapp.QueueUpdateDraw(func() {
				fmt.Fprint(a.chatView, "\n[red]+--------------------------------------------------+[white]\n")
				fmt.Fprint(a.chatView, "[red]|[white]  WARNING: RESET IDENTITY                      [red]|[white]\n")
				fmt.Fprint(a.chatView, "[red]+--------------------------------------------------+[white]\n")
				fmt.Fprintf(a.chatView, "[red]|[yellow]  File: %s\n", identity.KeyPath())
				fmt.Fprint(a.chatView, "[red]|[white]  Peer ID will change permanently.              [red]|[white]\n")
				fmt.Fprint(a.chatView, "[red]|[green]  Type /reset again[white] to confirm.                [red]|[white]\n")
				fmt.Fprint(a.chatView, "[red]|[dimgray]  Any other command cancels.                    [red]|[white]\n")
				fmt.Fprint(a.chatView, "[red]+--------------------------------------------------+[white]\n\n")
				a.chatView.ScrollToEnd()
				a.inputField.SetBorderColor(tcell.ColorRed).
					SetTitle(resetInputTitle)
			})
			return
		}

		a.mu.Lock()
		a.resetPending = false
		a.mu.Unlock()

		if err := identity.Delete(); err != nil {
			a.systemMsg(fmt.Sprintf("[red]Error deleting key: %v", err))
			return
		}

		a.restoreInputTitle()
		a.tapp.QueueUpdateDraw(func() {
			fmt.Fprint(a.chatView, "\n[green]identity.key deleted.[white]\n")
			fmt.Fprint(a.chatView, "[dimgray]Restart to generate a new Peer ID.[white]\n\n")
			a.chatView.ScrollToEnd()
		})

		go func() {
			time.Sleep(2 * time.Second)
			a.tapp.Stop()
		}()

	case "/connect":
		if len(parts) < 2 {
			a.systemMsg("usage: /connect <multiaddr>")
			return
		}

		ma, err := multiaddr.NewMultiaddr(parts[1])
		if err != nil {
			a.systemMsg("invalid address: " + err.Error())
			return
		}

		pi, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			a.systemMsg("parse error: " + err.Error())
			return
		}

		go func() {
			if err := a.host.Connect(context.Background(), *pi); err != nil {
				a.systemMsg("connect failed: " + err.Error())
				return
			}
			a.systemMsg(fmt.Sprintf("Connected to [green]%s", shortID(pi.ID)))
		}()

	case "/quit":
		a.tapp.Stop()

	default:
		a.systemMsg(fmt.Sprintf("Unknown command: %s  (try /help)", cmd))
	}
}
