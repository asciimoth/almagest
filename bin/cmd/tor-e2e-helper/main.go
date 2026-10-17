// Command tor-e2e-helper provides a deterministic Tor control and SOCKS
// fixture for sandbox integration tests. It is built only as a test payload.
package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
)

const (
	serverKey = "Tor safe cookie authentication server-to-controller hash"
	clientKey = "Tor safe cookie authentication controller-to-server hash"
)

type configuration struct {
	controlPort    string
	controlCookie  string
	upstream       string
	upstreamUser   string
	upstreamSecret string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	torrc, err := argument(arguments, "-f")
	if err != nil {
		return err
	}
	config, err := readConfiguration(torrc)
	if err != nil {
		return err
	}
	cookie := sha256.Sum256([]byte("almagest tor e2e helper cookie"))
	if err = os.WriteFile(config.controlCookie, cookie[:], 0o600); err != nil {
		return fmt.Errorf("write control cookie: %w", err)
	}
	socks, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen for SOCKS: %w", err)
	}
	defer socks.Close() //nolint:errcheck
	control, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen for control: %w", err)
	}
	defer control.Close() //nolint:errcheck
	if err = os.WriteFile(config.controlPort, []byte("PORT="+control.Addr().String()+"\n"), 0o600); err != nil {
		return fmt.Errorf("write control endpoint: %w", err)
	}
	stop := make(chan struct{})
	var once sync.Once
	go serveSOCKS(socks, config, stop)
	connection, err := control.Accept()
	if err != nil {
		return fmt.Errorf("accept control connection: %w", err)
	}
	err = serveControl(connection, cookie[:], socks.Addr().String(), config.upstream, func() { once.Do(func() { close(stop) }) })
	once.Do(func() { close(stop) })
	return err
}

func argument(arguments []string, name string) (string, error) {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == name {
			return arguments[index+1], nil
		}
	}
	return "", fmt.Errorf("required argument %s is absent", name)
}

func readConfiguration(name string) (configuration, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return configuration{}, fmt.Errorf("read torrc: %w", err)
	}
	config := configuration{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			value, err = strconv.Unquote(value)
			if err != nil {
				return configuration{}, fmt.Errorf("decode %s: %w", key, err)
			}
		}
		switch key {
		case "ControlPortWriteToFile":
			config.controlPort = value
		case "CookieAuthFile":
			config.controlCookie = value
		case "Socks5Proxy":
			config.upstream = value
		case "Socks5ProxyUsername":
			config.upstreamUser = value
		case "Socks5ProxyPassword":
			config.upstreamSecret = value
		}
	}
	if config.controlPort == "" || config.controlCookie == "" || config.upstream == "" || config.upstreamUser == "" || config.upstreamSecret == "" {
		return configuration{}, errors.New("torrc does not contain the required control and proxy settings")
	}
	return config, nil
}

func serveControl(connection net.Conn, cookie []byte, socks, upstream string, stop func()) error {
	defer connection.Close() //nolint:errcheck
	reader := bufio.NewReader(connection)
	var expectedAuth []byte
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read control command: %w", err)
		}
		command := strings.TrimSpace(line)
		var response string
		switch {
		case command == "PROTOCOLINFO 1":
			response = "250-PROTOCOLINFO 1\r\n250-AUTH METHODS=SAFECOOKIE\r\n250 OK\r\n"
		case strings.HasPrefix(command, "AUTHCHALLENGE SAFECOOKIE "):
			client, decodeErr := hex.DecodeString(strings.TrimPrefix(command, "AUTHCHALLENGE SAFECOOKIE "))
			if decodeErr != nil || len(client) != 32 {
				response = "513 invalid client nonce\r\n"
				break
			}
			server := sha256.Sum256(append([]byte("almagest tor e2e helper server nonce"), client...))
			expectedAuth = cookieMAC(clientKey, cookie, client, server[:])
			response = "250 AUTHCHALLENGE SERVERHASH=" + hex.EncodeToString(cookieMAC(serverKey, cookie, client, server[:])) + " SERVERNONCE=" + hex.EncodeToString(server[:]) + "\r\n"
		case strings.HasPrefix(command, "AUTHENTICATE "):
			value, decodeErr := hex.DecodeString(strings.TrimPrefix(command, "AUTHENTICATE "))
			if decodeErr != nil || !hmac.Equal(value, expectedAuth) {
				response = "515 authentication failed\r\n"
			} else {
				response = "250 OK\r\n"
			}
		case command == "GETINFO events/names":
			response = "250-events/names=STATUS_CLIENT HS_DESC\r\n250 OK\r\n"
		case command == "GETINFO status/bootstrap-phase":
			response = "250-status/bootstrap-phase=NOTICE BOOTSTRAP PROGRESS=100 TAG=done SUMMARY=Done\r\n250 OK\r\n"
		case command == "GETINFO net/listeners/socks":
			response = "250-net/listeners/socks=" + strconv.Quote(socks) + "\r\n250 OK\r\n"
		case command == "GETINFO status/circuit-established":
			response = "250-status/circuit-established=1\r\n250 OK\r\n"
		case command == "GETCONF Socks5Proxy":
			response = "250 Socks5Proxy=" + upstream + "\r\n"
		case command == "SIGNAL SHUTDOWN":
			response = "250 OK\r\n"
			if _, err = io.WriteString(connection, response); err != nil {
				return fmt.Errorf("write shutdown response: %w", err)
			}
			stop()
			return nil
		default:
			response = "250 OK\r\n"
		}
		if _, err = io.WriteString(connection, response); err != nil {
			return fmt.Errorf("write control response: %w", err)
		}
	}
}

func cookieMAC(key string, values ...[]byte) []byte {
	hash := hmac.New(sha256.New, []byte(key))
	for _, value := range values {
		_, _ = hash.Write(value)
	}
	return hash.Sum(nil)
}

func serveSOCKS(listener net.Listener, config configuration, stop <-chan struct{}) {
	go func() {
		<-stop
		_ = listener.Close()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer connection.Close() //nolint:errcheck
			_ = proxySOCKS(connection, config)
		}()
	}
}

func proxySOCKS(client net.Conn, config configuration) error {
	target, err := acceptSOCKS(client)
	if err != nil {
		return err
	}
	upstream, err := net.Dial("tcp", config.upstream)
	if err != nil {
		return err
	}
	defer upstream.Close() //nolint:errcheck
	if err = connectSOCKS(upstream, target, config.upstreamUser, config.upstreamSecret); err != nil {
		return err
	}
	if _, err = client.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return err
	}
	return relay(client, upstream)
}

func acceptSOCKS(connection net.Conn) (string, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(connection, header); err != nil || header[0] != 5 {
		return "", errors.New("invalid SOCKS greeting")
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(connection, methods); err != nil {
		return "", err
	}
	method := byte(0)
	if !strings.ContainsRune(string(methods), rune(method)) {
		method = 2
	}
	if _, err := connection.Write([]byte{5, method}); err != nil {
		return "", err
	}
	if method == 2 {
		if err := readUserPassword(connection); err != nil {
			return "", err
		}
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(connection, request); err != nil || request[0] != 5 || request[1] != 1 {
		return "", errors.New("invalid SOCKS request")
	}
	host, err := readSOCKSHost(connection, request[3])
	if err != nil {
		return "", err
	}
	port := make([]byte, 2)
	if _, err = io.ReadFull(connection, port); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port)))), nil
}

func readUserPassword(connection net.Conn) error {
	header := make([]byte, 2)
	if _, err := io.ReadFull(connection, header); err != nil || header[0] != 1 {
		return errors.New("invalid SOCKS authentication")
	}
	user := make([]byte, int(header[1]))
	if _, err := io.ReadFull(connection, user); err != nil {
		return err
	}
	if _, err := io.ReadFull(connection, header[:1]); err != nil {
		return err
	}
	secret := make([]byte, int(header[0]))
	if _, err := io.ReadFull(connection, secret); err != nil {
		return err
	}
	_, err := connection.Write([]byte{1, 0})
	return err
}

func readSOCKSHost(connection net.Conn, kind byte) (string, error) {
	size := 0
	switch kind {
	case 1:
		size = net.IPv4len
	case 4:
		size = net.IPv6len
	case 3:
		length := []byte{0}
		if _, err := io.ReadFull(connection, length); err != nil {
			return "", err
		}
		size = int(length[0])
	default:
		return "", errors.New("invalid SOCKS address type")
	}
	value := make([]byte, size)
	if _, err := io.ReadFull(connection, value); err != nil {
		return "", err
	}
	if kind == 3 {
		return string(value), nil
	}
	return net.IP(value).String(), nil
}

func connectSOCKS(connection net.Conn, target, user, secret string) error {
	if len(user) > 255 || len(secret) > 255 {
		return errors.New("upstream SOCKS credentials are too long")
	}
	if _, err := connection.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(connection, response); err != nil || response[0] != 5 || response[1] != 2 {
		return errors.New("upstream rejected SOCKS authentication")
	}
	auth := append([]byte{1, byte(len(user))}, []byte(user)...)
	auth = append(auth, byte(len(secret)))
	auth = append(auth, []byte(secret)...)
	if _, err := connection.Write(auth); err != nil {
		return err
	}
	if _, err := io.ReadFull(connection, response); err != nil || response[1] != 0 {
		return errors.New("upstream SOCKS authentication failed")
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return err
	}
	request := []byte{5, 1, 0}
	if address := net.ParseIP(host); address != nil {
		if ipv4 := address.To4(); ipv4 != nil {
			request = append(request, 1)
			request = append(request, ipv4...)
		} else {
			request = append(request, 4)
			request = append(request, address.To16()...)
		}
	} else {
		if len(host) > 255 {
			return errors.New("target host is too long")
		}
		request = append(request, 3, byte(len(host)))
		request = append(request, host...)
	}
	request = binary.BigEndian.AppendUint16(request, uint16(port))
	if _, err = connection.Write(request); err != nil {
		return err
	}
	header := make([]byte, 4)
	if _, err = io.ReadFull(connection, header); err != nil || header[0] != 5 || header[1] != 0 {
		return errors.New("upstream SOCKS connect failed")
	}
	_, err = readSOCKSHost(connection, header[3])
	if err != nil {
		return err
	}
	_, err = io.ReadFull(connection, make([]byte, 2))
	return err
}

func relay(left, right net.Conn) error {
	done := make(chan error, 2)
	copyConnection := func(destination, source net.Conn) {
		_, err := io.Copy(destination, source)
		done <- err
	}
	go copyConnection(left, right)
	go copyConnection(right, left)
	return <-done
}
