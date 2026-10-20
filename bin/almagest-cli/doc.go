package main

import (
	"context"
	"fmt"
	"io"

	"github.com/asciimoth/almagest/bin/api"
)

func runDocs(ctx context.Context, c *client, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printDocsHelp(stdout)
		return nil
	}
	switch args[0] {
	case "tree":
		fs := commandFlagSet("docs tree", stderr)
		locale := fs.String("locale", "en", "documentation locale")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if err := setDocsLocale(ctx, c, *locale); err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceDocs, string(api.MethodDocsTree), nil, stdout)
	case "page":
		fs := commandFlagSet("docs page", stderr)
		locale := fs.String("locale", "en", "documentation locale")
		id := fs.String("id", "", "stable documentation page ID")
		raw := fs.Bool("raw", false, "print only Markdown content")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("--id is required")
		}
		if err := setDocsLocale(ctx, c, *locale); err != nil {
			return err
		}
		if !*raw {
			return requestPretty(ctx, c, api.SurfaceDocs, string(api.MethodDocsPage), api.DocsPageRequest{ID: *id}, stdout)
		}
		result, err := c.request(ctx, api.SurfaceDocs, string(api.MethodDocsPage), api.DocsPageRequest{ID: *id})
		if err != nil {
			return err
		}
		page, err := api.DecodePayload[api.DocsPageResponse](result)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, page.Content)
		return err
	default:
		return fmt.Errorf("unknown docs command %q", args[0])
	}
}

func setDocsLocale(ctx context.Context, c *client, locale string) error {
	_, err := c.request(ctx, api.SurfaceAdmin, string(api.MethodAdminSetLocale), api.SetLocaleRequest{Locale: locale})
	return err
}

func printDocsHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  almagest-cli docs tree [--locale LOCALE]
  almagest-cli docs page --id ID [--locale LOCALE] [--raw]

Commands:
  tree   print the localized topic and page tree
  page   print one documentation page response or raw Markdown content
`)
}
