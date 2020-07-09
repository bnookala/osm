package configurator

import (
	"fmt"
	"reflect"

	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	k8s "github.com/open-service-mesh/osm/pkg/kubernetes"
)

// NewConfigurator implements configurator.Configurator and creates the Kubernetes client to manage namespaces.
func NewConfigurator(kubeClient kubernetes.Interface, stop chan struct{}, osmNamespace, osmConfigMapName string) Configurator {
	informerFactory := informers.NewSharedInformerFactory(kubeClient, k8s.DefaultKubeEventResyncInterval)
	informer := informerFactory.Core().V1().ConfigMaps().Informer()
	client := Client{
		informer:         informer,
		cache:            informer.GetStore(),
		cacheSynced:      make(chan interface{}),
		announcements:    make(chan interface{}),
		osmNamespace:     osmNamespace,
		osmConfigMapName: osmConfigMapName,
	}

	// Ensure this only watches the Namespace where OSM in installed
	shouldObserve := func(obj interface{}) bool {
		ns := reflect.ValueOf(obj).Elem().FieldByName("ObjectMeta").FieldByName("Namespace").String()
		return ns == osmNamespace
	}

	informerName := "ConfigMap"
	providerName := "OSMConfigMap"
	informer.AddEventHandler(k8s.GetKubernetesEventHandlers(informerName, providerName, client.announcements, shouldObserve))

	go client.run(stop)

	return &client
}

// This struct must match the shape of the "osm-config" ConfigMap
// which was created in the OSM namespace.
type osmConfig struct {

	// ConfigVersion is optional field, which shows the version of the config applied.
	// This is used for debug purposes.
	ConfigVersion int `yaml:"config_version"`

	// PermissiveTrafficPolicyMode is a bool toggle, which when TRUE ignores SMI policies and
	// allows existing Kubernetes services to communicate with each other uninterrupted.
	// This is useful whet set TRUE in brownfield configurations, where we first want to observe
	// existing traffic patterns.
	PermissiveTrafficPolicyMode bool `yaml:"permissive_traffic_policy_mode"`
}

func (c *Client) run(stop <-chan struct{}) {
	go c.informer.Run(stop)
	log.Info().Msgf("Started OSM ConfigMap informer - watching for %s", c.getConfigMapCacheKey())
	log.Info().Msg("[ConfigMap Client] Waiting for ConfigMap informer's cache to sync")
	if !cache.WaitForCacheSync(stop, c.informer.HasSynced) {
		log.Error().Msg("Failed initial cache sync for ConfigMap informer")
		return
	}

	// Closing the cacheSynced channel signals to the rest of the system that caches have been synced.
	close(c.cacheSynced)
	log.Info().Msg("[ConfigMap Client] Cache sync for ConfigMap informer finished")
}

func (c *Client) getConfigMapCacheKey() string {
	return fmt.Sprintf("%s/%s", c.osmNamespace, c.osmConfigMapName)
}

func (c *Client) getConfigMap() *osmConfig {
	configMapCacheKey := c.getConfigMapCacheKey()
	item, exists, err := c.cache.GetByKey(configMapCacheKey)
	if err != nil {
		log.Error().Err(err).Msgf("Error getting ConfigMap by key=%s from cache", configMapCacheKey)
	}

	if !exists {
		return &osmConfig{}
	}

	configMap := item.(*v1.ConfigMap)

	if len(configMap.Data) == 0 {
		log.Error().Msgf("The ConfigMap %s does not contain any Data", configMapCacheKey)
		return &osmConfig{}
	}

	var config []byte
	for _, cfg := range configMap.Data {
		config = []byte(cfg)
	}

	conf := osmConfig{}
	err = yaml.Unmarshal(config, &conf)
	if err != nil {
		log.Error().Err(err).Msgf("Error marshaling ConfigMap %s with content %s", c.osmConfigMapName, string(config))
	}

	return &conf
}