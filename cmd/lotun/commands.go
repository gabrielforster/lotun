package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/gabrielrocha/lotun/internal/client"
	"github.com/gabrielrocha/lotun/internal/config"
	"github.com/gabrielrocha/lotun/internal/protocol"
	"github.com/spf13/cobra"
)

// version is the CLI version. It defaults to "dev" and is overridden at build
// time via -ldflags "-X main.version=...".
var version = "dev"

// parsePort parses a TCP port string, requiring it to be in the valid range
// 1..65535. It is pure so that flag validation is unit-testable.
func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid port %q: not a number", s)
	}
	if p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid port %d: must be between 1 and 65535", p)
	}
	return p, nil
}

// validateTCPFlags enforces the tcp-specific flag rules before connecting:
// a private tunnel must list at least one allowed IP, and --password is not
// valid for tcp (privacy is controlled via --allow-ip). It is pure so that the
// rules are unit-testable without any networking.
func validateTCPFlags(private bool, allowIPs []string, password string) error {
	if password != "" {
		return errors.New("password is not valid for tcp: use allow_ips/--allow-ip for tcp privacy")
	}
	if private && len(allowIPs) == 0 {
		return errors.New("a private tcp tunnel requires at least one allowed IP (allow_ips/--allow-ip)")
	}
	return nil
}

// defaultConfigPath returns the default client config path (~/.lotun/config.yaml).
func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".lotun", "config.yaml")
	}
	return filepath.Join(home, ".lotun", "config.yaml")
}

// loadConnectedConfig loads the client config and validates that the control
// address and token are set, returning a clear error otherwise. Used by every
// command that talks to the server (all but login/version).
func loadConnectedConfig(path string) (config.ClientConfig, error) {
	c, err := config.LoadClient(path)
	if err != nil {
		return config.ClientConfig{}, err
	}
	if c.ControlAddr == "" || c.Token == "" {
		return config.ClientConfig{}, errors.New("not configured: run `lotun login` first")
	}
	return c, nil
}

// dial connects and authenticates to the control server using the given config.
func dial(c config.ClientConfig) (*client.Client, error) {
	return client.Connect(client.Options{
		ControlAddr: c.ControlAddr,
		Token:       c.Token,
		UseTLS:      c.TLS,
		TLSInsecure: c.TLSInsecure,
	})
}

// parseTunnelType maps a config `type:` value to a protocol tunnel type.
func parseTunnelType(s string) (protocol.TunnelType, error) {
	switch strings.ToLower(s) {
	case string(protocol.HTTP):
		return protocol.HTTP, nil
	case string(protocol.TCP):
		return protocol.TCP, nil
	default:
		return "", fmt.Errorf("invalid tunnel type %q: want %q or %q", s, protocol.HTTP, protocol.TCP)
	}
}

// tunnelRequests converts the declarative `tunnels:` config into registration
// requests, applying the same validation the http/tcp subcommands apply to
// their flags. It is pure so the rules are unit-testable without networking.
//
// DefaultDomain is deliberately not applied here: it would give every tunnel in
// the list the same subdomain, which the server rejects. Each entry names its
// own domain or lets the server assign one.
func tunnelRequests(tunnels []config.TunnelConfig) ([]client.TunnelRequest, error) {
	if len(tunnels) == 0 {
		return nil, errors.New("no tunnels configured: add a `tunnels:` list to the client config")
	}
	reqs := make([]client.TunnelRequest, 0, len(tunnels))
	for i, t := range tunnels {
		typ, err := parseTunnelType(t.Type)
		if err != nil {
			return nil, fmt.Errorf("tunnels[%d]: %w", i, err)
		}
		if t.Port < 1 || t.Port > 65535 {
			return nil, fmt.Errorf("tunnels[%d]: invalid port %d: must be between 1 and 65535", i, t.Port)
		}
		if typ == protocol.TCP {
			if err := validateTCPFlags(t.Private, t.AllowIPs, t.Password); err != nil {
				return nil, fmt.Errorf("tunnels[%d]: %w", i, err)
			}
		}
		reqs = append(reqs, client.TunnelRequest{
			Type:       typ,
			Domain:     t.Domain,
			LocalPort:  t.Port,
			RemotePort: t.RemotePort,
			Private:    t.Private,
			Password:   t.Password,
			AllowIPs:   t.AllowIPs,
		})
	}
	return reqs, nil
}

// newRootCmd builds the root command and wires all subcommands. The --config
// flag is persistent so every subcommand can read it.
func newRootCmd() *cobra.Command {
	var cfgPath string

	root := &cobra.Command{
		Use:           "lotun",
		Short:         "lotun exposes local services through a lotun server",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.PersistentFlags().StringVar(&cfgPath, "config", defaultConfigPath(), "path to client config file")

	root.AddCommand(
		newLoginCmd(&cfgPath),
		newHTTPCmd(&cfgPath),
		newTCPCmd(&cfgPath),
		newServeCmd(&cfgPath),
		newClaimCmd(&cfgPath),
		newUnclaimCmd(&cfgPath),
		newStatusCmd(&cfgPath),
		newVersionCmd(),
	)
	return root
}

func newLoginCmd(cfgPath *string) *cobra.Command {
	var server, token, defaultDomain string
	var useTLS, tlsInsecure bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Save server address and token to the client config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if server == "" || token == "" {
				return errors.New("both --server and --token are required")
			}
			if tlsInsecure && !useTLS {
				return errors.New("--tls-insecure requires --tls")
			}
			// Load first so an existing `tunnels:` list (and any flag the user
			// did not pass this time) survives re-running login.
			c, err := config.LoadClient(*cfgPath)
			if err != nil {
				return err
			}
			c.ControlAddr = server
			c.Token = token
			flags := cmd.Flags()
			if flags.Changed("default-domain") {
				c.DefaultDomain = defaultDomain
			}
			if flags.Changed("tls") {
				c.TLS = useTLS
			}
			if flags.Changed("tls-insecure") {
				c.TLSInsecure = tlsInsecure
			}
			if err := config.SaveClient(*cfgPath, c); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Saved config to %s\n", *cfgPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "control server address (host:port)")
	cmd.Flags().StringVar(&token, "token", "", "authentication token")
	cmd.Flags().StringVar(&defaultDomain, "default-domain", "", "default subdomain to request")
	cmd.Flags().BoolVar(&useTLS, "tls", false, "dial the control port over TLS")
	cmd.Flags().BoolVar(&tlsInsecure, "tls-insecure", false, "skip control certificate verification (self-signed certs; requires --tls)")
	return cmd
}

func newHTTPCmd(cfgPath *string) *cobra.Command {
	var domain, password string
	var private bool
	cmd := &cobra.Command{
		Use:   "http <port>",
		Short: "Expose a local HTTP port",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := parsePort(args[0])
			if err != nil {
				return err
			}
			cfg, err := loadConnectedConfig(*cfgPath)
			if err != nil {
				return err
			}
			if domain == "" {
				domain = cfg.DefaultDomain
			}
			return runTunnel(cmd, cfg, client.TunnelRequest{
				Type:      protocol.HTTP,
				Domain:    domain,
				LocalPort: port,
				Private:   private,
				Password:  password,
			})
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "requested subdomain (empty => server assigns)")
	cmd.Flags().BoolVar(&private, "private", false, "require a password to access the tunnel")
	cmd.Flags().StringVar(&password, "password", "", "password for a private tunnel (empty => server generates)")
	return cmd
}

func newTCPCmd(cfgPath *string) *cobra.Command {
	var domain, password string
	var remotePort int
	var private bool
	var allowIPs []string
	cmd := &cobra.Command{
		Use:   "tcp <port>",
		Short: "Expose a local TCP port",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTCPFlags(private, allowIPs, password); err != nil {
				return err
			}
			port, err := parsePort(args[0])
			if err != nil {
				return err
			}
			cfg, err := loadConnectedConfig(*cfgPath)
			if err != nil {
				return err
			}
			if domain == "" {
				domain = cfg.DefaultDomain
			}
			return runTunnel(cmd, cfg, client.TunnelRequest{
				Type:       protocol.TCP,
				Domain:     domain,
				LocalPort:  port,
				RemotePort: remotePort,
				Private:    private,
				AllowIPs:   allowIPs,
			})
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "requested subdomain (empty => server assigns)")
	cmd.Flags().IntVar(&remotePort, "remote-port", 0, "requested remote port (0 => server assigns)")
	cmd.Flags().BoolVar(&private, "private", false, "restrict access to --allow-ip addresses")
	cmd.Flags().StringSliceVar(&allowIPs, "allow-ip", nil, "IP allowed to connect (repeatable; required with --private)")
	cmd.Flags().StringVar(&password, "password", "", "not valid for tcp")
	return cmd
}

// runTunnel connects, registers a single tunnel, prints its public address,
// then serves inbound streams until the process receives SIGINT/SIGTERM. A
// dropped control connection ends the command; `lotun serve` is the variant
// that reconnects.
func runTunnel(cmd *cobra.Command, cfg config.ClientConfig, req client.TunnelRequest) error {
	ctx, stop := signalContext(cmd)
	defer stop()

	err := serveOnce(ctx, cmd.OutOrStdout(), cfg, []client.TunnelRequest{req})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// signalContext derives a context from the command that is cancelled on
// SIGINT/SIGTERM.
func signalContext(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
}

// serveOnce dials the control server, registers every request on that one
// session, prints the public addresses, and serves inbound streams until ctx is
// cancelled or the connection drops.
func serveOnce(ctx context.Context, out io.Writer, cfg config.ClientConfig, reqs []client.TunnelRequest) error {
	cl, err := dial(cfg)
	if err != nil {
		return err
	}
	defer cl.Close()

	for _, req := range reqs {
		reg, err := cl.Register(req)
		if err != nil {
			return err
		}
		printRegistered(out, req, reg)
	}
	fmt.Fprintln(out, "Forwarding traffic. Press Ctrl-C to stop.")

	return cl.Serve(ctx)
}

// printRegistered reports one established tunnel and its public address.
func printRegistered(out io.Writer, req client.TunnelRequest, reg protocol.Registered) {
	if req.Type == protocol.HTTP {
		fmt.Fprintf(out, "Tunnel ready: %s -> localhost:%d\n", reg.PublicURL, req.LocalPort)
	} else {
		fmt.Fprintf(out, "Tunnel ready: %s:%d -> localhost:%d\n", reg.Host, reg.Port, req.LocalPort)
	}
	if reg.GeneratedPassword != "" {
		fmt.Fprintf(out, "Generated password: %s\n", reg.GeneratedPassword)
	}
}

// Reconnect backoff bounds for `lotun serve`.
const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

// serveWithRetry runs serveOnce, re-dialing and re-registering with capped
// exponential backoff whenever the control connection drops, until ctx is
// cancelled. A session that outlived maxBackoff is treated as healthy, so a
// long-lived tunnel does not stay pinned at the slowest retry interval.
func serveWithRetry(ctx context.Context, out io.Writer, cfg config.ClientConfig, reqs []client.TunnelRequest) error {
	backoff := minBackoff
	for {
		start := time.Now()
		err := serveOnce(ctx, out, cfg, reqs)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) > maxBackoff {
			backoff = minBackoff
		}
		fmt.Fprintf(out, "Connection lost (%v); reconnecting in %v\n", err, backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func newServeCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Serve every tunnel declared in the client config, reconnecting on failure",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConnectedConfig(*cfgPath)
			if err != nil {
				return err
			}
			reqs, err := tunnelRequests(cfg.Tunnels)
			if err != nil {
				return err
			}
			ctx, stop := signalContext(cmd)
			defer stop()
			return serveWithRetry(ctx, cmd.OutOrStdout(), cfg, reqs)
		},
	}
}

func newClaimCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "claim <name>",
		Short: "Claim ownership of a subdomain name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConnectedConfig(*cfgPath)
			if err != nil {
				return err
			}
			cl, err := dial(cfg)
			if err != nil {
				return err
			}
			defer cl.Close()
			if err := cl.Claim(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Claimed %q\n", args[0])
			return nil
		},
	}
}

func newUnclaimCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "unclaim <name>",
		Short: "Release ownership of a subdomain name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConnectedConfig(*cfgPath)
			if err != nil {
				return err
			}
			cl, err := dial(cfg)
			if err != nil {
				return err
			}
			defer cl.Close()
			if err := cl.Unclaim(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Unclaimed %q\n", args[0])
			return nil
		},
	}
}

func newStatusCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "List active tunnels",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConnectedConfig(*cfgPath)
			if err != nil {
				return err
			}
			cl, err := dial(cfg)
			if err != nil {
				return err
			}
			defer cl.Close()
			tunnels, err := cl.ListTunnels()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(tunnels) == 0 {
				fmt.Fprintln(out, "No active tunnels.")
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SUBDOMAIN\tTYPE\tPUBLIC\tLOCAL PORT")
			for _, t := range tunnels {
				public := t.PublicURL
				if public == "" {
					public = strconv.Itoa(t.Port)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", t.Subdomain, t.Type, public, t.LocalPort)
			}
			return w.Flush()
		},
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the lotun version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version)
		},
	}
}
