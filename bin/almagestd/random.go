package main

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
)

type daemonRandomSeeds struct {
	system    int64
	workspace int64
}

func newDaemonRandomSeeds() (daemonRandomSeeds, error) {
	seeds, err := readDaemonRandomSeeds(rand.Reader)
	if err != nil {
		return daemonRandomSeeds{}, err
	}
	return overrideDaemonRandomSeeds(seeds)
}

func readDaemonRandomSeeds(random io.Reader) (daemonRandomSeeds, error) {
	var raw [16]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return daemonRandomSeeds{}, fmt.Errorf("read cryptographic random seeds: %w", err)
	}
	return daemonRandomSeeds{
		system:    int64(binary.LittleEndian.Uint64(raw[:8])),
		workspace: int64(binary.LittleEndian.Uint64(raw[8:])),
	}, nil
}
