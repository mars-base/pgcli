package cli

import (
	"crypto/rand"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

const (
	randDefaultLength = 24
	randMaxLength     = 4096
	randDefaultCount  = 1
	randMaxCount      = 1000

	randCharsetAlnum     = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	randCharsetAlpha     = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	randCharsetLower     = "abcdefghijklmnopqrstuvwxyz"
	randCharsetUpper     = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	randCharsetDigits    = "0123456789"
	randCharsetHex       = "0123456789abcdef"
	randCharsetBase64URL = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"
	randCharsetPassword  = randCharsetAlnum + "!@#$%^&*()-_=+[]{}<>?:;,.~"
)

var randCharsets = map[string]string{
	"alnum":     randCharsetAlnum,
	"alpha":     randCharsetAlpha,
	"lower":     randCharsetLower,
	"upper":     randCharsetUpper,
	"digits":    randCharsetDigits,
	"hex":       randCharsetHex,
	"base64url": randCharsetBase64URL,
	"password":  randCharsetPassword,
}

var (
	randLength     int
	randCount      int
	randCharset    string
	randExtraChars string
)

func init() {
	randCmd.Flags().IntVarP(&randLength, "length", "l", randDefaultLength, "number of characters per string (1-4096)")
	randCmd.Flags().IntVarP(&randCount, "count", "n", randDefaultCount, "number of strings to print, one per line (1-1000)")
	randCmd.Flags().StringVar(&randCharset, "charset", "alnum", "character set: alnum, alpha, lower, upper, digits, hex, base64url, password")
	randCmd.Flags().StringVar(&randExtraChars, "extra-chars", "", "additional printable ASCII characters to include in the selected set")
	rootCmd.AddCommand(randCmd)
}

var randCmd = &cobra.Command{
	Use:   "rand",
	Short: "Generate cryptographically secure random strings",
	Long: `Generate random strings for passwords, API tokens and secrets. Characters
come from crypto/rand; output is raw, one string per line, so it can be used
directly in shell assignments.

Length defaults to 24. Character sets:

  alnum      a-z A-Z 0-9 (default)
  alpha      a-z A-Z
  lower      a-z
  upper      A-Z
  digits     0-9
  hex        0-9 a-f
  base64url  a-z A-Z 0-9 - _ (URL- and JWT-safe)
  password   alnum plus !@#$%^&*()-_=+[]{}<>?:;,.~

Use --extra-chars to add your own printable ASCII characters to any set.
Passwords containing shell metacharacters must be quoted when used in shell
commands or SQL.

Examples:
  pg rand
  pg rand -l 32
  pg rand -l 32 --charset password
  pg rand -n 5 -l 48 --charset hex
  pg rand -l 48 --charset base64url --extra-chars '.:'
  TOKEN=$(pg rand -l 48)`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateRandLength(randLength); err != nil {
			return err
		}
		if err := validateRandCount(randCount); err != nil {
			return err
		}
		charset, err := buildRandCharset(randCharset, randExtraChars)
		if err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		for i := 0; i < randCount; i++ {
			s, err := randomString(charset, randLength)
			if err != nil {
				return fmt.Errorf("failed to generate random string: %w", err)
			}
			fmt.Fprintln(out, s)
		}
		return nil
	},
}

func validateRandLength(n int) error {
	if n < 1 || n > randMaxLength {
		return fmt.Errorf("--length must be between 1 and %d (got %d)", randMaxLength, n)
	}
	return nil
}

func validateRandCount(n int) error {
	if n < 1 || n > randMaxCount {
		return fmt.Errorf("--count must be between 1 and %d (got %d)", randMaxCount, n)
	}
	return nil
}

// buildRandCharset resolves a named preset and appends any extra characters,
// dropping duplicates so every character keeps the same probability.
func buildRandCharset(name, extra string) (string, error) {
	base, ok := randCharsets[name]
	if !ok {
		return "", fmt.Errorf("unknown charset %q (choose from %s)", name, strings.Join(randCharsetNames(), ", "))
	}
	if extra == "" {
		return base, nil
	}

	seen := make(map[rune]bool, len(base))
	for _, r := range base {
		seen[r] = true
	}
	var b strings.Builder
	b.WriteString(base)
	for _, r := range extra {
		if r < 0x21 || r > 0x7e {
			return "", fmt.Errorf("--extra-chars must contain printable ASCII characters only (got %q)", r)
		}
		if seen[r] {
			continue
		}
		seen[r] = true
		b.WriteRune(r)
	}
	return b.String(), nil
}

func randCharsetNames() []string {
	return []string{"alnum", "alpha", "lower", "upper", "digits", "hex", "base64url", "password"}
}

// randomString picks characters with rejection sampling so byte values beyond
// the largest multiple of len(charset) never introduce modulo bias.
func randomString(charset string, length int) (string, error) {
	if charset == "" {
		return "", fmt.Errorf("charset must not be empty")
	}
	if len(charset) > 256 {
		return "", fmt.Errorf("charset must contain at most 256 characters")
	}

	limit := 256 - 256%len(charset)
	out := make([]byte, length)
	buf := make([]byte, length+16)
	for i := 0; i < length; {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out[i] = charset[int(b)%len(charset)]
			i++
			if i == length {
				break
			}
		}
	}
	return string(out), nil
}
