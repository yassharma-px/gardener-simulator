package store

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/yassharma/gardener-simulator/pkg/types"
)

// ShootStore manages simulated Shoot clusters
type ShootStore struct {
	mu       sync.RWMutex
	shoots   map[string]map[string]*types.Shoot // namespace -> name -> shoot
	projects map[string]*types.ProjectConfig    // project name -> config
}

// NewShootStore creates a new ShootStore
func NewShootStore() *ShootStore {
	return &ShootStore{
		shoots:   make(map[string]map[string]*types.Shoot),
		projects: make(map[string]*types.ProjectConfig),
	}
}

// LoadFromConfig loads shoots from configuration
func (s *ShootStore) LoadFromConfig(cfg *types.SimulatorConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range cfg.Projects {
		project := &cfg.Projects[i]
		namespace := project.Namespace
		if namespace == "" {
			namespace = fmt.Sprintf("garden-%s", project.Name)
		}
		project.Namespace = namespace
		s.projects[project.Name] = project

		if s.shoots[namespace] == nil {
			s.shoots[namespace] = make(map[string]*types.Shoot)
		}

		for _, shootCfg := range project.Shoots {
			shoot := s.createShoot(namespace, shootCfg)
			s.shoots[namespace][shootCfg.Name] = shoot
		}
	}
}

func (s *ShootStore) createShoot(namespace string, cfg types.ShootConfig) *types.Shoot {
	// Determine hibernation state based on status
	hibernated := cfg.Status == types.ShootStatusHibernated

	return &types.Shoot{
		APIVersion: "core.gardener.cloud/v1beta1",
		Kind:       "Shoot",
		Metadata: types.ShootMetadata{
			Name:              cfg.Name,
			Namespace:         namespace,
			UID:               uuid.New().String(),
			Labels:            cfg.Labels,
			CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		},
		Spec: types.ShootSpec{
			SeedName:     cfg.SeedName,
			Provider:     types.ProviderSpec{Type: cfg.CloudType},
			Region:       "us-east-1",
			CloudProfile: cfg.CloudType, // Use cloud type as cloud profile name
			Kubernetes:   &types.KubernetesSpec{Version: "1.32.0"},
			Hibernation:  &types.HibernationSpec{Enabled: hibernated},
		},
		Status: types.ShootStatusInfo{
			SeedName:   cfg.SeedName,
			Hibernated: hibernated,
			Conditions: []types.Condition{
				{Type: "APIServerAvailable", Status: "True"},
				{Type: "ControlPlaneHealthy", Status: "True"},
				{Type: "SystemComponentsHealthy", Status: "True"},
			},
			LastOperation: &types.LastOperation{
				Type:     "Reconcile",
				State:    "Succeeded",
				Progress: 100,
			},
		},
	}
}

// ListShoots returns all shoots in a namespace, optionally filtered by label selector
func (s *ShootStore) ListShoots(namespace, labelSelector string) (*types.ShootList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := &types.ShootList{
		APIVersion: "core.gardener.cloud/v1beta1",
		Kind:       "ShootList",
		Items:      []types.Shoot{},
	}

	labels := parseLabelSelector(labelSelector)

	// If namespace is empty, list across all namespaces (cluster-scoped)
	if namespace == "" {
		for _, nsMap := range s.shoots {
			for _, shoot := range nsMap {
				if matchesLabels(shoot.Metadata.Labels, labels) {
					list.Items = append(list.Items, *shoot)
				}
			}
		}
		return list, nil
	}

	// List shoots in specific namespace
	nsMap, ok := s.shoots[namespace]
	if !ok {
		return list, nil
	}

	for _, shoot := range nsMap {
		if matchesLabels(shoot.Metadata.Labels, labels) {
			list.Items = append(list.Items, *shoot)
		}
	}

	return list, nil
}

// GetShoot returns a specific shoot
func (s *ShootStore) GetShoot(namespace, name string) (*types.Shoot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if nsMap, ok := s.shoots[namespace]; ok {
		if shoot, ok := nsMap[name]; ok {
			return shoot, nil
		}
	}
	return nil, fmt.Errorf("shoot %s/%s not found", namespace, name)
}

// AddShoot dynamically adds a shoot
func (s *ShootStore) AddShoot(namespace string, cfg types.ShootConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.shoots[namespace] == nil {
		s.shoots[namespace] = make(map[string]*types.Shoot)
	}
	s.shoots[namespace][cfg.Name] = s.createShoot(namespace, cfg)
}

// DeleteShoot removes a shoot
func (s *ShootStore) DeleteShoot(namespace, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if nsMap, ok := s.shoots[namespace]; ok {
		delete(nsMap, name)
	}
}

func parseLabelSelector(selector string) map[string]string {
	labels := make(map[string]string)
	if selector == "" {
		return labels
	}
	parts := strings.Split(selector, ",")
	for _, part := range parts {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			labels[kv[0]] = kv[1]
		}
	}
	return labels
}

func matchesLabels(shootLabels, selector map[string]string) bool {
	for k, v := range selector {
		if shootLabels[k] != v {
			return false
		}
	}
	return true
}

// ListProjects returns all projects
func (s *ShootStore) ListProjects() []types.ProjectConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	projects := make([]types.ProjectConfig, 0, len(s.projects))
	for _, p := range s.projects {
		projects = append(projects, *p)
	}
	return projects
}

// GetProject returns a project by name
func (s *ShootStore) GetProject(name string) (*types.ProjectConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if p, ok := s.projects[name]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("project %s not found", name)
}

// UpdateShootStatus updates the status of a specific shoot
func (s *ShootStore) UpdateShootStatus(namespace, name string, status types.ShootStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if nsMap, ok := s.shoots[namespace]; ok {
		if shoot, ok := nsMap[name]; ok {
			// Update conditions based on status
			switch status {
			case types.ShootStatusHealthy:
				shoot.Status.Conditions = []types.Condition{
					{Type: "APIServerAvailable", Status: "True"},
					{Type: "ControlPlaneHealthy", Status: "True"},
					{Type: "SystemComponentsHealthy", Status: "True"},
				}
			case types.ShootStatusUnhealthy:
				shoot.Status.Conditions = []types.Condition{
					{Type: "APIServerAvailable", Status: "False"},
					{Type: "ControlPlaneHealthy", Status: "False"},
					{Type: "SystemComponentsHealthy", Status: "False"},
				}
			case types.ShootStatusProgressing:
				shoot.Status.Conditions = []types.Condition{
					{Type: "APIServerAvailable", Status: "Unknown"},
					{Type: "ControlPlaneHealthy", Status: "Unknown"},
					{Type: "SystemComponentsHealthy", Status: "Unknown"},
				}
			case types.ShootStatusHibernated:
				shoot.Status.Conditions = []types.Condition{
					{Type: "HibernationPossible", Status: "True"},
					{Type: "Hibernated", Status: "True"},
				}
			}
			return nil
		}
	}
	return fmt.Errorf("shoot %s/%s not found", namespace, name)
}

// GetShootStatus returns the status of a specific shoot
func (s *ShootStore) GetShootStatus(namespace, name string) (types.ShootStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if nsMap, ok := s.shoots[namespace]; ok {
		if shoot, ok := nsMap[name]; ok {
			// Determine status from conditions
			for _, cond := range shoot.Status.Conditions {
				if cond.Type == "Hibernated" && cond.Status == "True" {
					return types.ShootStatusHibernated, nil
				}
				if cond.Type == "APIServerAvailable" {
					if cond.Status == "True" {
						return types.ShootStatusHealthy, nil
					} else if cond.Status == "False" {
						return types.ShootStatusUnhealthy, nil
					}
					return types.ShootStatusProgressing, nil
				}
			}
			return types.ShootStatusHealthy, nil
		}
	}
	return "", fmt.Errorf("shoot %s/%s not found", namespace, name)
}

// ListNamespaces returns all unique namespaces that contain shoots
func (s *ShootStore) ListNamespaces() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	namespaces := make([]string, 0, len(s.shoots))
	for ns := range s.shoots {
		namespaces = append(namespaces, ns)
	}
	return namespaces
}
