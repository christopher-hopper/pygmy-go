package library

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/fubarhouse/pygmy-go/service/dnsmasq"
	"github.com/fubarhouse/pygmy-go/service/haproxy"
	model "github.com/fubarhouse/pygmy-go/service/interface"
	"github.com/fubarhouse/pygmy-go/service/interface/docker"
	"github.com/fubarhouse/pygmy-go/service/mailhog"
	"github.com/fubarhouse/pygmy-go/service/network"
	"github.com/fubarhouse/pygmy-go/service/resolv"
	"github.com/fubarhouse/pygmy-go/service/ssh/agent"
	"github.com/fubarhouse/pygmy-go/service/ssh/key"
	"github.com/spf13/viper"
)

// ImportDefaults is an exported function which allows third-party applications
// to provide their own *Service and integrate it with their application so
// that Pygmy is more extendable via API. It's here so that we have one common
// import functionality that respects the users' decision to import config
// defaults in a centralized way.
func ImportDefaults(c *Config, service string, importer model.Service) bool {
	if _, ok := c.Services[service]; ok {

		container := c.Services[service]

		// If configuration has a value for the defaults label
		if val, ok := container.Config.Labels["pygmy.defaults"]; ok {
			if val == "1" || val == "true" {
				// Clear destination Service to a new nil value.
				c.Services[service] = model.Service{}
				// Import the provided Service to the map entry.
				c.Services[service] = getService(importer, c.Services[service])
				// This is now successful, so return true.
				return true
			}
		}

		// If container has a value for the defaults label
		if defaultsNeeded, _ := container.GetFieldBool("defaults"); defaultsNeeded {
			c.Services[service] = getService(importer, c.Services[service])
			return true
		}

		// If default configuration has a value for the defaults label
		if val, ok := importer.Config.Labels["pygmy.defaults"]; ok {
			if val == "1" || val == "true" {
				c.Services[service] = getService(importer, c.Services[service])
				return true
			}
		}
	} else {
		if defaultsNeeded, _ := importer.GetFieldBool("defaults"); defaultsNeeded {
			c.Services[service] = getService(importer, c.Services[service])
			return true
		}
	}

	return false
}

// Setup holds the core of configuration management with Pygmy.
// It will merge in all the configurations and provide defaults.
func Setup(c *Config) {

	// All Viper API calls for default values go here.

	// Set default value for default inheritance:
	viper.SetDefault("defaults", true)

	// Set the default domain.
	viper.SetDefault("domain", "docker.amazee.io")
	if c.Domain == "" {
		c.Domain = viper.GetString("domain")
	}

	// Resolvers don't have hard defaults defined which
	// are mergable. So we set them in viper before
	// unmarshalling the config so that config specified
	// will override the default, but the default won't
	// be overridden if it's not specified.
	if viper.GetBool("defaults") {

		var ResolvMacOS = resolv.Resolv{
			Data:    "# Generated by amazeeio pygmy\nnameserver 127.0.0.1\nport 6053\n",
			Enabled: true,
			File:    c.Domain,
			Folder:  "/etc/resolver",
			Name:    "MacOS Resolver",
		}

		var ResolvLinux = resolv.Resolv{
			Data:    fmt.Sprintf("# Generated by amazeeio pygmy\n[Resolve]\nDNS=127.0.0.1:6053\nDomains=%s\n", c.Domain),
			Enabled: true,
			File:    c.Domain,
			Folder:  "/usr/lib/systemd/resolved.conf.d",
			Name:    "Linux Resolver",
		}

		if runtime.GOOS == "darwin" {
			viper.SetDefault("resolvers", []resolv.Resolv{
				ResolvMacOS,
			})
		} else if runtime.GOOS == "linux" {
			viper.SetDefault("resolvers", []resolv.Resolv{
				ResolvLinux,
			})
		} else if runtime.GOOS == "windows" {
			viper.SetDefault("resolvers", []resolv.Resolv{})
		}
	}

	e := viper.Unmarshal(&c)

	if e != nil {
		fmt.Println(e)
	}

	if c.Defaults {

		// If Services have been provided in complete or partially,
		// this will override the defaults allowing any value to
		// be changed by the user in the configuration file ~/.pygmy.yml
		if c.Services == nil || len(c.Services) == 0 {
			c.Services = make(map[string]model.Service, 6)
		}

		ImportDefaults(c, "amazeeio-ssh-agent", agent.New())
		ImportDefaults(c, "amazeeio-ssh-agent-add-key", key.NewAdder())
		ImportDefaults(c, "amazeeio-dnsmasq", dnsmasq.New(&model.Params{Domain: c.Domain}))
		ImportDefaults(c, "amazeeio-haproxy", haproxy.New(&model.Params{Domain: c.Domain}))
		ImportDefaults(c, "amazeeio-mailhog", mailhog.New(&model.Params{Domain: c.Domain}))

		// We need Port 80 to be configured by default.
		// If a port on amazeeio-haproxy isn't explicitly declared,
		// then we should set this value. This is far more creative
		// than needed, so feel free to revisit if you can compile it.
		if c.Services["amazeeio-haproxy"].HostConfig.PortBindings == nil {
			c.Services["amazeeio-haproxy"] = getService(haproxy.NewDefaultPorts(), c.Services["amazeeio-haproxy"])
		}

		// It's sensible to use the same logic for port 1025.
		// If a user needs to configure it, the default value should not be set also.
		if c.Services["amazeeio-mailhog"].HostConfig.PortBindings == nil {
			c.Services["amazeeio-mailhog"] = getService(mailhog.NewDefaultPorts(), c.Services["amazeeio-mailhog"])
		}

		// Ensure Networks has a at least a zero value.
		// We should provide defaults for amazeeio-network when no value is provided.
		if c.Networks == nil {
			c.Networks = make(map[string]types.NetworkResource)
			c.Networks["amazeeio-network"] = getNetwork(network.New(), c.Networks["amazeeio-network"])
		}

		// Ensure Volumes has a at least a zero value.
		if c.Volumes == nil {
			c.Volumes = make(map[string]types.Volume)
		}

		for _, v := range c.Volumes {
			// Get the potentially existing volume:
			c.Volumes[v.Name], _ = docker.DockerVolumeGet(v.Name)
			// Merge the volume with the provided configuration:
			c.Volumes[v.Name] = getVolume(c.Volumes[v.Name], c.Volumes[v.Name])
		}
	}

	// Mandatory validation check.
	for id, service := range c.Services {
		if name, err := service.GetFieldString("name"); err != nil && name != "" {
			fmt.Printf("service '%v' does not have have a value for label 'pygmy.name'\n", id)
			os.Exit(2)
		}
		if service.Config.Image == "" {
			fmt.Printf("service '%v' does not have have a value for {{.Config.Image}}\n", id)
			os.Exit(2)
		}
	}

	c.SortedServices = make([]string, 0, len(c.Services))
	for key, service := range c.Services {
		weight, _ := service.GetFieldInt("weight")
		c.SortedServices = append(c.SortedServices, fmt.Sprintf("%06d|%v", weight, key))
	}
	sort.Strings(c.SortedServices)

	for n, v := range c.SortedServices {
		c.SortedServices[n] = strings.Split(v, "|")[1]
	}

}
