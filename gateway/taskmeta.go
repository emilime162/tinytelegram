package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ResolveSelfAddr reads ECS task metadata v4 and returns (selfID, grpcAddr).
// selfID is the last 8 chars of the task-ARN (short, stable, unique per task).
// grpcAddr is this container's private IPv4 plus the given port — suitable
// for storing in Redis `gateways` hash so peer gateways can dial back.
func ResolveSelfAddr(metaURI, grpcPort string) (string, string, error) {
	if metaURI == "" {
		return "", "", errors.New("ECS_CONTAINER_METADATA_URI_V4 not set")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(metaURI + "/task")
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("metadata HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}

	var meta struct {
		TaskARN    string `json:"TaskARN"`
		Containers []struct {
			Name     string `json:"Name"`
			Networks []struct {
				IPv4Addresses []string `json:"IPv4Addresses"`
			} `json:"Networks"`
		} `json:"Containers"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return "", "", fmt.Errorf("parse metadata: %w", err)
	}
	arnParts := strings.Split(meta.TaskARN, "/")
	last := arnParts[len(arnParts)-1]
	if len(last) < 8 {
		return "", "", fmt.Errorf("unexpected task ARN: %s", meta.TaskARN)
	}
	selfID := last[len(last)-8:]

	if len(meta.Containers) == 0 || len(meta.Containers[0].Networks) == 0 ||
		len(meta.Containers[0].Networks[0].IPv4Addresses) == 0 {
		return "", "", errors.New("no container IP in metadata")
	}
	ip := meta.Containers[0].Networks[0].IPv4Addresses[0]
	return selfID, fmt.Sprintf("%s:%s", ip, grpcPort), nil
}
