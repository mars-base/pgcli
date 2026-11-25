package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/podman"
	"github.com/mars-base/pgcli/internal/safeline"
)

// ---------------------------------------------------------------------------
//
// TLS certificates live in the console and are bound to sites by id. These
// subcommands upload PEM pairs and list/delete them through the console API;
// `site create --cert/--key` uploads and binds in one step.

var addonSafelineCertCmd = &cobra.Command{
	Use:   "cert",
	Short: "List, upload and delete TLS certificates",
	Long: `Manage the TLS certificates stored in a SafeLine console.

Uploaded certificates are PEM certificate/key pairs (the certificate may be a
full chain). The console derives the covered domains and the expiry from the
PEM. A certificate bound to a site's TLS entry port (a port suffixed _ssl)
cannot be deleted until that site is updated or removed.

Subcommands:
  list     List stored certificates
  create   Upload a certificate and key file
  update   Replace a certificate's PEM pair in place (renewal)
  delete   Delete certificates by id

Examples:
  pg addon safeline cert list
  pg addon safeline cert create --cert fullchain.pem --key privkey.pem
  pg addon safeline cert update --cert-id 2 --cert fullchain.pem --key privkey.pem
  pg addon safeline cert delete --cert-id 2`,
}

var addonSafelineCertListCmd = &cobra.Command{
	Use:   "list",
	Short: "List stored TLS certificates",
	Long: `List every certificate stored in the console with its id, covered domains,
type, expiry and the sites currently using it. The ids are what
'site create --cert-id' takes.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineCertList(cmd)
	},
}

var addonSafelineCertCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Upload a TLS certificate and key",
	Long: `Upload a PEM certificate (leaf or full chain) and its PEM private key to
the console. The console parses the certificate and prints the id that
'site create --cert-id' takes; 'site create --cert/--key' does both in one
step.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineCertCreate(cmd)
	},
}

var addonSafelineCertUpdateCmd = &cobra.Command{
	Use:   "update --cert-id <id>",
	Short: "Replace a stored certificate's PEM pair (renewal)",
	Long: `Replace the certificate and private key of an existing certificate in place.

Every site bound to that certificate serves the new one once the console
publishes its config, so this is the renewal path: upload the renewed PEM with
the same --cert-id and no site needs to be touched. To bind a site to a
different certificate instead, use 'site update --site-id <id> --cert-id <new id>'.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineCertUpdate(cmd)
	},
}

var addonSafelineCertDeleteCmd = &cobra.Command{
	Use:   "delete --cert-id <id> [--cert-id <id>...]",
	Short: "Delete TLS certificates",
	Long: `Delete one or more stored certificates by id (--cert-id, repeatable or
comma-separated). The console refuses to delete a certificate that a site's
TLS entry port is using; remove the site or point it at another certificate
first.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineCertDelete(cmd)
	},
}

func runAddonSafelineCertList(cmd *cobra.Command) error {
	client, sc, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	certs, err := client.ListCerts()
	if err != nil {
		return err
	}
	if len(certs) == 0 {
		fmt.Printf("No certificates on %q (console %s).\n", sc.Name, podman.ConsoleURL(&sc))
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tDOMAINS\tTYPE\tSTATUS\tEXPIRES\tUSED-BY")
	for _, c := range certs {
		status := "ok"
		switch {
		case c.Expired:
			status = "expired"
		case c.Revoked:
			status = "revoked"
		case !c.Trusted:
			status = "untrusted"
		}
		usedBy := "-"
		if len(c.RelatedSites) > 0 {
			usedBy = strings.Join(c.RelatedSites, ",")
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n",
			c.ID,
			strings.Join(c.Domains, ","),
			safeline.CertTypeName(c.Type),
			status,
			c.ValidBefore,
			usedBy,
		)
	}
	return w.Flush()
}

func runAddonSafelineCertCreate(cmd *cobra.Command) error {
	fs := cmd.Flags()
	certPath, _ := fs.GetString("cert")
	keyPath, _ := fs.GetString("key")
	if certPath == "" || keyPath == "" {
		return fmt.Errorf("--cert and --key are required (PEM certificate and private key files)")
	}
	crt, key, err := safeline.LoadCertificate(certPath, keyPath)
	if err != nil {
		return err
	}
	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	id, err := client.UploadCert(crt, key)
	if err != nil {
		return fmt.Errorf("uploading certificate: %w", err)
	}
	fmt.Printf("✓ Uploaded SafeLine certificate %d\n", id)
	fmt.Printf("  Files:  %s, %s\n", certPath, keyPath)
	fmt.Printf("  Bind:   pg addon safeline site create --cert-id %d ...\n", id)
	return nil
}

func runAddonSafelineCertUpdate(cmd *cobra.Command) error {
	fs := cmd.Flags()
	id, _ := fs.GetInt("cert-id")
	certPath, _ := fs.GetString("cert")
	keyPath, _ := fs.GetString("key")
	if id < 1 {
		return fmt.Errorf("--cert-id is required (list the ids with 'pg addon safeline cert list')")
	}
	if certPath == "" || keyPath == "" {
		return fmt.Errorf("--cert and --key are required (PEM certificate and private key files)")
	}
	crt, key, err := safeline.LoadCertificate(certPath, keyPath)
	if err != nil {
		return err
	}
	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	if err := client.UpdateCert(id, crt, key); err != nil {
		return fmt.Errorf("updating certificate %d: %w", id, err)
	}
	fmt.Printf("✓ Updated SafeLine certificate %d\n", id)
	fmt.Printf("  Files:  %s, %s\n", certPath, keyPath)
	fmt.Printf("  Note:   every site bound to it now serves the new certificate\n")
	return nil
}

func runAddonSafelineCertDelete(cmd *cobra.Command) error {
	raw, _ := cmd.Flags().GetIntSlice("cert-id")
	if len(raw) == 0 {
		return fmt.Errorf("at least one --cert-id is required (list them with 'pg addon safeline cert list')")
	}
	ids := make([]int, 0, len(raw))
	for _, id := range raw {
		if id < 1 {
			return fmt.Errorf("invalid certificate id %d", id)
		}
		ids = append(ids, id)
	}
	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := client.DeleteCert(id); err != nil {
			return fmt.Errorf("deleting certificate %d: %w", id, err)
		}
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.Itoa(id))
	}
	fmt.Printf("✓ Deleted SafeLine certificate(s): %s\n", strings.Join(parts, ", "))
	return nil
}
