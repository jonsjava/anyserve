package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/jonsjava/anyserve/internal/api"
	"github.com/jonsjava/anyserve/internal/app"
	"github.com/jonsjava/anyserve/internal/remote"
	"github.com/jonsjava/anyserve/internal/webui"
)

func proxyCmd() *cobra.Command {
	var listen string
	var noOpen bool
	cmd := &cobra.Command{
		Use:   "proxy [host]",
		Short: "Open the web UI on this computer, for this host or a remote one",
		Long: `proxy serves the web UI on your own 127.0.0.1; nothing is exposed to the
network.

With no host, it's the UI for this machine, and you're its owner (an admin).

With a host added by ` + "`anyserve remote add`" + `, the browser's connection travels
through the SSH tunnel and is encrypted all the way to the host (HTTPS on
127.0.0.1). The host checks your browser's client certificate there: admins
need one (` + "`anyserve users cert`" + ` on the host) to use admin powers.`,
		Example: `  anyserve proxy           # this machine
  anyserve proxy box       # a remote host`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := signalContext()
			defer cancel()
			if len(args) == 0 {
				return proxyLocal(ctx, cmd, a, listen, noOpen)
			}
			return proxyRemote(ctx, cmd, a, args[0], listen, noOpen)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "", "local address (default 127.0.0.1:7080; this machine's own UI uses 127.0.0.1:7081)")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "don't open a browser")
	return cmd
}

func listenLoopback(addr, def string) (net.Listener, error) {
	if addr == "" {
		addr = def
	}
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(h); h != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("the proxy listens on this computer only: give a 127.0.0.1 address")
	}
	return net.Listen("tcp", addr)
}

func proxyLocal(ctx context.Context, cmd *cobra.Command, a *app.App, listen string, noOpen bool) error {
	s, audit, err := apiServer(a)
	if err != nil {
		return err
	}
	defer audit.Close()
	b := make([]byte, 24)
	rand.Read(b)
	token := hex.EncodeToString(b)
	l, err := listenLoopback(listen, "127.0.0.1:7081")
	if err != nil {
		return err
	}
	handler := s.Handler()
	// The running service holds the fleet's connections; use it when it's up.
	if c, err := localAPI(a); err == nil {
		handler = c.proxy()
	}
	srv := &http.Server{
		Handler:           webui.Handler(handler, webui.FixedToken(token)),
		ReadHeaderTimeout: 30 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return api.WithCaller(ctx, app.Owner) },
	}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	url := fmt.Sprintf("http://%s/#token=%s", l.Addr(), token)
	fmt.Fprintf(cmd.OutOrStdout(), "This machine's web UI: %s\n(The address carries a one-time token; keep it to yourself.) Ctrl-C to stop.\n", url)
	if !noOpen {
		openBrowser(url)
	}
	if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func proxyRemote(ctx context.Context, cmd *cobra.Command, a *app.App, name, listen string, noOpen bool) error {
	c, err := connect(a, name)
	if err != nil {
		return err
	}
	defer c.Close()
	resp, err := remote.APIClient(c).Post(remote.APIURL+"/v1/web-session", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: the host's web UI isn't available (%s); is its Anyserve up to date?", name, resp.Status)
	}
	var sess remote.WebSession
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		return err
	}
	caPath := filepath.Join(a.Dirs.Config, "hosts", name+"-web-ca.pem")
	os.MkdirAll(filepath.Dir(caPath), 0o700)
	if err := os.WriteFile(caPath, []byte(sess.CA), 0o644); err != nil {
		return err
	}
	l, err := listenLoopback(listen, "127.0.0.1:7080")
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	url := fmt.Sprintf("https://127.0.0.1:%s/#token=%s", port, sess.Token)
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s's web UI: %s\n", name, url)
	fmt.Fprintf(out, "The token in that address works until %s; keep it to yourself.\n", sess.Expires.Local().Format(time.DateTime))
	fmt.Fprintf(out, "Your browser will warn about the certificate until you trust %s's web CA, saved at\n  %s\n", name, caPath)
	fmt.Fprintln(out, "(Settings → Privacy and security → Manage certificates → Authorities → Import, trusted for websites. It only covers localhost addresses.)")
	fmt.Fprintln(out, "Ctrl-C to stop.")
	if !noOpen {
		openBrowser(url)
	}
	for {
		lc, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go tunnel(c, lc, cmd.ErrOrStderr())
	}
}

func tunnel(c *ssh.Client, lc net.Conn, errOut io.Writer) {
	rc, err := c.Dial("tcp", net.JoinHostPort(remote.WebHost, fmt.Sprint(remote.WebPort)))
	if err != nil {
		fmt.Fprintf(errOut, "web UI connection: %v\n", err)
		lc.Close()
		return
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(rc, lc); rc.Close(); done <- struct{}{} }()
	go func() { io.Copy(lc, rc); lc.Close(); done <- struct{}{} }()
	<-done
	<-done
}

func openBrowser(url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return
		}
		c = exec.Command("xdg-open", url)
	}
	c.Start()
}
