package main

// platform.go — платформо-зависимые утилиты.
//
// Весь основной код (main.go) уже кросс-платформенный:
//   - os.UserHomeDir()   работает на Linux, macOS, Windows
//   - filepath.Join()    использует правильный разделитель на каждой ОС
//   - tcell/tview        поддерживает Linux, macOS, Windows (cmd/powershell/wt)
//   - libp2p TCP         работает везде
//   - golang.org/x/net/proxy SOCKS5 — работает везде
//
// Единственное отличие по платформам — путь к identity.key:
//   Linux:   ~/.p2pmessenger/identity.key
//   macOS:   ~/.p2pmessenger/identity.key
//   Windows: C:\Users\<user>\.p2pmessenger\identity.key
//
// os.UserHomeDir() + filepath.Join() обрабатывают это автоматически.

// torDefaultAddr возвращает дефолтный адрес Tor для текущей платформы.
// На всех платформах Tor слушает на 127.0.0.1:9050 по умолчанию.
func torDefaultAddr() string {
	return "127.0.0.1:9050"
}
