package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/jiangfire/snapnotes/internal/storage/sqlite"
	"github.com/jiangfire/snapnotes/internal/tui"
)

func main() {
	databasePath := filepath.Join(".", "snapnotes.db")
	store, err := sqlite.Open(databasePath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	model, err := tui.NewHomeModel(store, nil, nil)
	if err != nil {
		log.Fatal(err)
	}

	if _, err := tea.NewProgram(model).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
