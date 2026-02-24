package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yassharma/gardener-simulator/pkg/server"
	"github.com/yassharma/gardener-simulator/pkg/types"
	"gopkg.in/yaml.v3"
)

func main() {
	configFile := flag.String("config", "", "Path to configuration file")
	port := flag.Int("port", 8443, "Server port")
	certDir := flag.String("cert-dir", "./certs", "Directory for certificates")
	numShoots := flag.Int("shoots", 10, "Number of shoots to generate per project")
	numProjects := flag.Int("projects", 1, "Number of projects to generate")
	flag.Parse()

	var config *types.SimulatorConfig

	if *configFile != "" {
		// Load config from file
		data, err := os.ReadFile(*configFile)
		if err != nil {
			log.Fatalf("Failed to read config file: %v", err)
		}
		config = &types.SimulatorConfig{}
		if err := yaml.Unmarshal(data, config); err != nil {
			log.Fatalf("Failed to parse config file: %v", err)
		}
	} else {
		// Generate default config
		config = generateDefaultConfig(*numProjects, *numShoots)
	}

	config.Port = *port
	config.CertDir = *certDir

	srv, err := server.NewServer(config)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Stop(ctx); err != nil {
			log.Printf("Error during shutdown: %v", err)
		}
		os.Exit(0)
	}()

	if err := srv.Start(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func generateDefaultConfig(numProjects, numShoots int) *types.SimulatorConfig {
	config := &types.SimulatorConfig{
		KubeconfigTTL: 24 * time.Hour,
		Projects:      make([]types.ProjectConfig, 0, numProjects),
		ErrorInjection: types.ErrorInjectionConfig{
			Enabled: false,
		},
	}

	cloudTypes := []string{"aws", "gcp", "azure"}
	seedNames := []string{"aws-eu1", "gcp-us1", "azure-eu1"}

	for p := 0; p < numProjects; p++ {
		projectName := fmt.Sprintf("project-%d", p)

		project := types.ProjectConfig{
			Name:      projectName,
			Namespace: "garden-" + projectName,
			Shoots:    make([]types.ShootConfig, 0, numShoots),
		}

		for s := 0; s < numShoots; s++ {
			shoot := types.ShootConfig{
				Name:      fmt.Sprintf("shoot-%d", s),
				SeedName:  seedNames[s%len(seedNames)],
				CloudType: cloudTypes[s%len(cloudTypes)],
				Status:    types.ShootStatusHealthy,
				Labels: map[string]string{
					"environment": []string{"dev", "staging", "prod"}[s%3],
					"team":        []string{"platform", "data", "web"}[s%3],
				},
			}
			project.Shoots = append(project.Shoots, shoot)
		}

		config.Projects = append(config.Projects, project)
	}

	return config
}
