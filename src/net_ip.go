package main

import (
	"fmt"
	"net"
	"os"

	"github.com/gofiber/fiber/v2"
)

func GetIpFromAwsEc2Metadata() (string, error) {
	// get the local ipv4 address of the ec2 instance via the meta-data url
	agent := fiber.Get("http://169.254.169.254/latest/meta-data/local-ipv4")
	if err := agent.Parse(); err != nil {
		return "", err
	} else {
		code, body, errs := agent.Bytes()
		if len(errs) > 0 {
			return "", errs[0]
		}
		if code == 200 {
			localAddress := string(body)
			// validate if the local address is a valid ipv4 address
			if net.ParseIP(localAddress) != nil {
				return localAddress, nil
			} else {
				// return new error if the local address is not a valid ipv4 address
				return "", fmt.Errorf("invalid local ipv4 address")
			}
		}
		return "", fmt.Errorf("failed to get local ipv4 address. Metadata endpoint response with code: %d", code)
	}
}

type EcsNetworkInterface struct {
	NetworkMode     string   `json:"NetworkMode"`
	IPv4Addresses   []string `json:"IPv4Addresses"`
	AttachmentIndex int      `json:"AttachmentIndex"`
	MACAddress      string   `json:"MACAddress"`
}
type EcsContainerMetadata struct {
	Cluster                string                `json:"Cluster"`
	TaskARN                string                `json:"TaskARN"`
	TaskDefinitionFamily   string                `json:"TaskDefinitionFamily"`
	TaskDefinitionRevision string                `json:"TaskDefinitionRevision"`
	ContainerName          string                `json:"ContainerName"`
	ContainerID            string                `json:"ContainerID"`
	ContainerInstanceARN   string                `json:"ContainerInstanceARN"`
	ImageID                string                `json:"ImageID"`
	ImageName              string                `json:"ImageName"`
	DesiredStatus          string                `json:"DesiredStatus"`
	KnownStatus            string                `json:"KnownStatus"`
	CreatedAt              string                `json:"CreatedAt"`
	StartedAt              string                `json:"StartedAt"`
	Type                   string                `json:"Type"`
	Networks               []EcsNetworkInterface `json:"Networks"`
	HealthStatus           string                `json:"HealthStatus"`
}

func GetIpFromAwsEcsContainerMetadata() (string, error) {
	metadataUri := os.Getenv("ECS_CONTAINER_METADATA_URI")
	if metadataUri == "" {
		return "", fmt.Errorf("ECS_CONTAINER_METADATA_URI is not set")
	}
	// get the local ipv4 address of the ecs container via the meta-data url
	agent := fiber.Get(metadataUri)
	if err := agent.Parse(); err != nil {
		return "", err
	} else {
		code, body, errs := agent.Bytes()
		if len(errs) > 0 {
			return "", errs[0]
		}
		if code == 200 {
			// parse the task metadata
			var taskMetadata EcsContainerMetadata
			if err := json.Unmarshal(body, &taskMetadata); err != nil {
				return "", err
			}
			// get the network interfaces and list of ipv4 addresses
			if taskMetadata.Networks == nil || len(taskMetadata.Networks) == 0 {
				return "", fmt.Errorf("no network interfaces found")
			}
			iPv4Addresses := taskMetadata.Networks[0].IPv4Addresses
			if len(iPv4Addresses) == 0 {
				return "", fmt.Errorf("no ipv4 addresses found")
			}
			// get the first ipv4 address
			localAddress := iPv4Addresses[0]
			// validate if the local address is a valid ipv4 address
			if net.ParseIP(localAddress) != nil {
				return localAddress, nil
			} else {
				// return new error if the local address is not a valid ipv4 address
				return "", fmt.Errorf("invalid local ipv4 address")
			}
		}
		return "", fmt.Errorf("failed to get local ipv4 address. Metadata endpoint response with code: %d", code)
	}
}

func GetIpFromHostNetInterfaces() (string, error) {
	// get the local ipv4 address of the host machine
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	} else {
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				// check if IPv4 or IPv6 is not nil
				if ipnet.IP.To4() != nil {
					// get the first ipv4 address
					// this is already a valid ipv4 address
					return ipnet.IP.String(), nil
				}
				// TODO: skip ipv6 for now
				// if ipnet.IP.To16() != nil {}
			}
		}
		return "", fmt.Errorf("failed to get local ipv4 address. No valid ipv4 address found")
	}
}
