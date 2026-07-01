package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"text/tabwriter"

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
		return errors.New("--password is not valid for tcp: use --allow-ip for tcp privacy")
	}
	if private && len(allowIPs) == 0 {
		return errors.New("--private tcp requires at least one --allow-ip")
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
	})
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
		newClaimCmd(&cfgPath),
		newUnclaimCmd(&cfgPath),
		newStatusCmd(&cfgPath),
		newVersionCmd(),
	)
	return root
}

func newLoginCmd(cfgPath *string) *cobra.Command {
	var server, token, defaultDomain string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Save server address and token to the client config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if server == "" || token == "" {
				return errors.New("both --server and --token are required")
			}
			c := config.ClientConfig{
				ControlAddr:   server,
				Token:         token,
				DefaultDomain: defaultDomain,
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

// runTunnel connects, registers the tunnel, prints its public address, then
// serves inbound streams until the process receives SIGINT/SIGTERM.
func runTunnel(cmd *cobra.Command, cfg config.ClientConfig, req client.TunnelRequest) error {
	cl, err := dial(cfg)
	if err != nil {
		return err
	}
	defer cl.Close()

	reg, err := cl.Register(req)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if req.Type == protocol.HTTP {
		fmt.Fprintf(out, "Tunnel ready: %s\n", reg.PublicURL)
	} else {
		fmt.Fprintf(out, "Tunnel ready: %s:%d\n", reg.Host, reg.Port)
	}
	if reg.GeneratedPassword != "" {
		fmt.Fprintf(out, "Generated password: %s\n", reg.GeneratedPassword)
	}
	fmt.Fprintln(out, "Forwarding traffic. Press Ctrl-C to stop.")

	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	err = cl.Serve(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
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
