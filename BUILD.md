# Сборка под все платформы

## Требования
- Go 1.21+
- `go mod tidy` перед первой сборкой

---

## Linux

```bash
go mod tidy
go build -o messenger .
./messenger
./messenger --tor 127.0.0.1:9050   # с Tor
```

**Tor установка:**
```bash
sudo apt install tor          # Debian/Ubuntu
sudo dnf install tor          # Fedora
sudo pacman -S tor            # Arch
sudo systemctl start tor
```

---

## macOS

```bash
go mod tidy
go build -o messenger .
./messenger
./messenger --tor 127.0.0.1:9050
```

**Tor установка:**
```bash
brew install tor
brew services start tor
```

**TUI работает в:** Terminal.app, iTerm2, Warp, Alacritty.

---

## Windows

```powershell
go mod tidy
go build -o messenger.exe .
.\messenger.exe
.\messenger.exe --tor 127.0.0.1:9050
```

**TUI работает в:** Windows Terminal (рекомендуется), PowerShell 7+.
**Не работает в:** старый cmd.exe (нет поддержки ANSI цветов).

**Tor установка:**
1. Скачать Tor Expert Bundle: https://www.torproject.org/download/tor/
2. Распаковать, запустить `tor.exe`
3. По умолчанию слушает на `127.0.0.1:9050`

**Важно для Windows:** если видишь кракозябры вместо рамок — включи UTF-8:
```powershell
chcp 65001
```

---

## Кросс-компиляция (собрать под другую ОС)

```bash
# На Linux собрать под Windows
GOOS=windows GOARCH=amd64 go build -o messenger.exe .

# На Linux собрать под macOS
GOOS=darwin GOARCH=amd64 go build -o messenger-mac .
GOOS=darwin GOARCH=arm64 go build -o messenger-mac-m1 .  # Apple Silicon

# На macOS собрать под Linux
GOOS=linux GOARCH=amd64 go build -o messenger-linux .
```

---

## Что работает везде без изменений

| Компонент | Linux | macOS | Windows |
|-----------|-------|-------|---------|
| libp2p TCP transport | ✅ | ✅ | ✅ |
| mDNS (локальная сеть) | ✅ | ✅ | ✅* |
| Kademlia DHT | ✅ | ✅ | ✅ |
| TUI (tcell/tview) | ✅ | ✅ | ✅** |
| identity.key (сохранение) | ✅ | ✅ | ✅ |
| E2E шифрование | ✅ | ✅ | ✅ |
| Tor (SOCKS5) | ✅ | ✅ | ✅ |

*На Windows mDNS может требовать разрешения брандмауэра.
**Windows Terminal или PowerShell 7+ обязательны для корректного TUI.
