package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/deckhouse/deckhouse/dhctl/pkg/kubernetes/client"
	"github.com/deckhouse/deckhouse/dhctl/pkg/log"
	"github.com/deckhouse/deckhouse/dhctl/pkg/util/retry"
)

const (
	candiDir = "/deckhouse/candi"

	providerSchemaFilenameSuffix = "_configuration.yaml"
)

func numerateManifestLines(manifest []byte) string {
	manifestLines := strings.Split(string(manifest), "\n")
	builder := strings.Builder{}

	for index, line := range manifestLines {
		builder.WriteString(fmt.Sprintf("%d\t%s\n", index+1, line))
	}

	return builder.String()
}

func ParseConfig(path string) (*MetaConfig, error) {
	fileContent, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("loading config file: %v", err)
	}

	return ParseConfigFromData(string(fileContent))
}

func ParseConfigFromCluster(kubeCl *client.KubernetesClient) (*MetaConfig, error) {
	var metaConfig *MetaConfig
	var err error
	err = log.Process("common", "Get Cluster configuration", func() error {
		return retry.StartLoop("Get Cluster configuration from Kubernetes cluster", 10, 5, func() error {
			metaConfig, err = parseConfigFromCluster(kubeCl)
			return err
		})
	})
	if err != nil {
		return nil, err
	}
	return metaConfig, nil
}

func ParseConfigInCluster(kubeCl *client.KubernetesClient) (*MetaConfig, error) {
	var metaConfig *MetaConfig
	var err error

	err = retry.StartSilentLoop("Get Cluster configuration from inside Kubernetes cluster", 5, 5, func() error {
		metaConfig, err = parseConfigFromCluster(kubeCl)
		return err
	})
	if err != nil {
		return nil, err
	}
	return metaConfig, nil
}

func parseConfigFromCluster(kubeCl *client.KubernetesClient) (*MetaConfig, error) {
	metaConfig := MetaConfig{}
	schemaStore := NewSchemaStore()

	clusterConfig, err := kubeCl.CoreV1().Secrets("kube-system").Get(context.TODO(), "d8-cluster-configuration", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	clusterConfigData := clusterConfig.Data["cluster-configuration.yaml"]
	_, err = schemaStore.Validate(&clusterConfigData)
	if err != nil {
		return nil, err
	}

	var parsedClusterConfig map[string]json.RawMessage
	if err := yaml.Unmarshal(clusterConfigData, &parsedClusterConfig); err != nil {
		return nil, err
	}

	metaConfig.ClusterConfig = parsedClusterConfig

	var clusterType string
	if err := json.Unmarshal(parsedClusterConfig["clusterType"], &clusterType); err != nil {
		return nil, err
	}

	if clusterType == CloudClusterType {
		providerClusterConfig, err := kubeCl.CoreV1().Secrets("kube-system").Get(context.TODO(), "d8-provider-cluster-configuration", metav1.GetOptions{})
		if err != nil {
			return nil, err
		}

		providerClusterConfigData := providerClusterConfig.Data["cloud-provider-cluster-configuration.yaml"]
		_, err = schemaStore.Validate(&providerClusterConfigData)
		if err != nil {
			return nil, err
		}

		var parsedProviderClusterConfig map[string]json.RawMessage
		if err := yaml.Unmarshal(providerClusterConfigData, &parsedProviderClusterConfig); err != nil {
			return nil, err
		}

		metaConfig.ProviderClusterConfig = parsedProviderClusterConfig
	}

	return metaConfig.Prepare()
}

func ParseConfigFromData(configData string) (*MetaConfig, error) {
	schemaStore := NewSchemaStore()

	bigFileTmp := strings.TrimSpace(configData)
	docs := regexp.MustCompile(`(?:^|\s*\n)---\s*`).Split(bigFileTmp, -1)

	metaConfig := MetaConfig{}
	for _, doc := range docs {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}

		docData := []byte(doc)

		index, err := schemaStore.Validate(&docData)
		if err != nil {
			return nil, fmt.Errorf("config validation: %v\ndata: \n%s\n", err, numerateManifestLines(docData))
		}

		var data map[string]json.RawMessage
		if err = yaml.Unmarshal(docData, &data); err != nil {
			return nil, fmt.Errorf("config unmarshal: %v\ndata: \n%s\n", err, numerateManifestLines(docData))
		}

		switch {
		case index.Kind == "InitConfiguration":
			metaConfig.InitClusterConfig = data
		case index.Kind == "ClusterConfiguration":
			metaConfig.ClusterConfig = data
		case index.Kind == "StaticClusterConfiguration":
			metaConfig.StaticClusterConfig = data
		case strings.HasSuffix(index.Kind, "ClusterConfiguration"):
			metaConfig.ProviderClusterConfig = data
		}
	}

	return metaConfig.Prepare()
}
