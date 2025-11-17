package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/koltyakov/gosip"
	edgeondemand "github.com/vba-excel/sp-edgeondemand"
	"github.com/vba-excel/spapi"
)

func main() {
	var (
		cfgPath = flag.String("cfg", "private.json", "caminho para private.json")
		siteURL = flag.String("site", "", "override do Site URL (opcional)")
		list    = flag.String("list", "", "nome da lista (título OU caminho server-relative)")
		itemID  = flag.Int("id", 0, "ID do item")
		file    = flag.String("file", "", "ficheiro a anexar")
		tout    = flag.Int("t", 60, "timeout em segundos")
	)
	flag.Parse()

	if *list == "" || *itemID <= 0 || *file == "" {
		log.Fatalf("uso: go run . -cfg private.json -site https://tenant.sharepoint.com/sites/XYZ -list MinhaLista -id 123 -file C:\\Temp\\teste.txt")
	}

	auth := &edgeondemand.AuthCnfg{}
	if err := auth.ReadConfig(*cfgPath); err != nil {
		log.Fatalf("ler %s: %v", *cfgPath, err)
	}
	if *siteURL != "" {
		auth.SiteURL = *siteURL
	}

	httpTransport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   10 * time.Second,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	httpClient := http.Client{
		Timeout:   30 * time.Second,
		Transport: httpTransport,
	}

	spClient := &gosip.SPClient{
		Client:     httpClient,
		AuthCnfg:   auth,
		ConfigPath: *cfgPath,
	}
	svc := spapi.New(spClient)

	fh, err := os.Open(*file)
	if err != nil {
		log.Fatalf("abrir ficheiro: %v", err)
	}
	defer fh.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*tout)*time.Second)
	defer cancel()

	baseName := filepath.Base(*file)
	log.Printf("A anexar %q ao item %d da lista %q ...", baseName, *itemID, *list)

	info, err := svc.AddAttachmentRaw(ctx, *list, *itemID, baseName, fh)
	if err != nil {
		log.Fatalf("AddAttachmentRaw falhou: %v", err)
	}

	fmt.Println("✓ Anexo adicionado com sucesso:")
	fmt.Printf("  FileName: %s\n", info.FileName)
	fmt.Printf("  ServerRelativeURL: %s\n", info.ServerRelativeURL)
}
