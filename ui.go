package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rivo/tview"

	"p2p-messenger/internal/domain/chat"
)

func (a *App) buildTUI() {
	a.tapp = tview.NewApplication()

	a.chatView = tview.NewTextView().SetDynamicColors(true).SetScrollable(true).SetWordWrap(true)
	a.chatView.SetBorder(true).SetBorderColor(tcell.ColorTeal).
		SetTitle(" Messages ").SetTitleColor(tcell.ColorAqua).
		SetBackgroundColor(tcell.ColorBlack)

	a.inputField = tview.NewInputField().
		SetLabel(" > ").SetLabelColor(tcell.ColorGreen).
		SetFieldWidth(0).SetFieldBackgroundColor(tcell.ColorBlack).
		SetFieldTextColor(tcell.ColorWhite)
	a.inputField.SetBorder(true).SetBorderColor(tcell.ColorTeal).
		SetTitle(defaultInputTitle).SetTitleColor(tcell.ColorDimGray)
	a.inputField.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			text := a.inputField.GetText()
			a.inputField.SetText("")
			go a.handleInput(text)
		}
	})

	a.sidebar = tview.NewList().ShowSecondaryText(true).
		SetHighlightFullLine(true).
		SetSelectedBackgroundColor(tcell.ColorTeal).
		SetSelectedTextColor(tcell.ColorBlack)
	a.sidebar.SetBorder(true).SetBorderColor(tcell.ColorTeal).
		SetTitle(" Rooms ").SetTitleColor(tcell.ColorAqua).
		SetBackgroundColor(tcell.ColorBlack)
	a.sidebar.SetSelectedFunc(func(_ int, main, _ string, _ rune) {
		room := strings.TrimPrefix(main, "# ")
		a.mu.Lock()
		a.curRoom = room
		a.mu.Unlock()
		go a.updateStatus()
	})

	a.statusBar = tview.NewTextView().SetDynamicColors(true)
	a.statusBar.SetBackgroundColor(tcell.ColorDarkSlateGray)

	left := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.chatView, 0, 1, false).
		AddItem(a.inputField, 3, 0, true)
	main := tview.NewFlex().
		AddItem(a.sidebar, 22, 0, false).
		AddItem(left, 0, 1, true)
	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.buildHeader(), 3, 0, false).
		AddItem(main, 0, 1, true).
		AddItem(a.statusBar, 1, 0, false)

	a.tapp.SetRoot(root, true).EnableMouse(true)
	a.tapp.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyTab {
			if a.tapp.GetFocus() == a.inputField {
				a.tapp.SetFocus(a.sidebar)
			} else {
				a.tapp.SetFocus(a.inputField)
			}
			return nil
		}

		return event
	})
}

func (a *App) buildHeader() *tview.TextView {
	view := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	view.SetBackgroundColor(tcell.ColorTeal)

	torTag := ""
	if a.torMode {
		torTag = " | Tor ON"
	}

	fmt.Fprintf(view, "\n[black::b]P2P MESSENGER   [dimgray]id: %s[black]   nick: [black::b]%s%s",
		shortID(a.host.ID()), a.nickname, torTag)
	return view
}

func (a *App) updateStatus() {
	if a.tapp == nil {
		return
	}

	now := time.Now()
	a.mu.Lock()
	peers := a.peerCount
	room := a.curRoom
	members := chat.CountOnline(a.roomMembers[room], now)
	rooms := make([]string, len(a.rooms))
	copy(rooms, a.rooms)
	roomMembers := make(map[string]int, len(a.roomMembers))
	for roomName, membersByPeer := range a.roomMembers {
		roomMembers[roomName] = chat.CountOnline(membersByPeer, now)
	}
	encrypted := a.roomKeys[room] != nil
	a.mu.Unlock()

	encTag := ""
	if encrypted {
		encTag = "  [green]E2E[white]"
	}

	torTag := ""
	if a.torMode {
		torTag = "  [green]Tor[white]"
	}

	a.tapp.QueueUpdateDraw(func() {
		a.statusBar.Clear()
		fmt.Fprintf(a.statusBar,
			"  [green]*[white] peers:[yellow]%d[white]  room:[cyan]%s[white]  members:[green]%d[white]%s%s  [dimgray]TAB=focus /who=online /help",
			peers, room, members+1, encTag, torTag)

		a.sidebar.Clear()
		for index, roomName := range rooms {
			memberCount := roomMembers[roomName] + 1
			a.sidebar.AddItem("# "+roomName, fmt.Sprintf("  %d members", memberCount), 0, nil)
			if roomName == room {
				a.sidebar.SetCurrentItem(index)
			}
		}
	})
}

func (a *App) appendChat(message chat.Message) {
	timestamp := message.Timestamp.Format("15:04:05")
	shortPeer := shortID(peer.ID(message.From))

	nameColor := "[cyan]"
	if message.From == a.host.ID().String() {
		nameColor = "[green]"
	}

	a.mu.Lock()
	encrypted := a.roomKeys[message.Room] != nil
	a.mu.Unlock()

	lockTag := ""
	if encrypted {
		lockTag = "[green][E2E][white] "
	}

	a.tapp.QueueUpdateDraw(func() {
		fmt.Fprintf(a.chatView, "[dimgray]%s %s%s%s[white]: %s\n[dimgray]         -> %s[white]\n",
			timestamp, lockTag, nameColor, message.Nickname, message.Text, shortPeer)
		a.chatView.ScrollToEnd()
	})
}

func (a *App) systemMsg(text string) {
	if a.tapp == nil {
		return
	}

	a.tapp.QueueUpdateDraw(func() {
		fmt.Fprintf(a.chatView, "[yellow]  * %s[white]\n", text)
		a.chatView.ScrollToEnd()
	})
}
