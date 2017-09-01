// Copyright 2017 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package state

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/juju/errors"
	"github.com/juju/utils/set"
	"gopkg.in/juju/names.v2"

	"github.com/juju/juju/environs"
	"github.com/juju/juju/instance"
	"github.com/juju/juju/network"
)

func (st *State) getModelSubnets() (set.Strings, error) {
	subnets, err := st.AllSubnets()
	if err != nil {
		return nil, errors.Trace(err)
	}
	modelSubnetIds := make(set.Strings)
	for _, subnet := range subnets {
		modelSubnetIds.Add(string(subnet.ProviderId()))
	}
	return modelSubnetIds, nil
}

// ReloadSpaces loads spaces and subnets from provider specified by environ into state.
// Currently it's an append-only operation, no spaces/subnets are deleted.
func (st *State) ReloadSpaces(environ environs.Environ) error {
	netEnviron, ok := environs.SupportsNetworking(environ)
	if !ok {
		return errors.NotSupportedf("spaces discovery in a non-networking environ")
	}
	canDiscoverSpaces, err := netEnviron.SupportsSpaceDiscovery()
	if err != nil {
		return errors.Trace(err)
	}
	if canDiscoverSpaces {
		spaces, err := netEnviron.Spaces()
		if err != nil {
			return errors.Trace(err)
		}
		return errors.Trace(st.SaveSpacesFromProvider(spaces))
	} else {
		logger.Debugf("environ does not support space discovery, falling back to subnet discovery")
		subnets, err := netEnviron.Subnets(instance.UnknownId, nil)
		if err != nil {
			return errors.Trace(err)
		}
		return errors.Trace(st.SaveSubnetsFromProvider(subnets, ""))
	}
}

// SaveSubnetsFromProvider loads subnets into state.
// Currently it does not delete removed subnets.
func (st *State) SaveSubnetsFromProvider(subnets []network.SubnetInfo, spaceName string) error {
	modelSubnetIds, err := st.getModelSubnets()
	if err != nil {
		return errors.Trace(err)
	}

	for _, subnet := range subnets {
		if modelSubnetIds.Contains(string(subnet.ProviderId)) {
			continue
		}
		var firstZone string
		if len(subnet.AvailabilityZones) > 0 {
			firstZone = subnet.AvailabilityZones[0]
		}
		_, err := st.AddSubnet(SubnetInfo{
			ProviderId:        subnet.ProviderId,
			ProviderNetworkId: subnet.ProviderNetworkId,
			CIDR:              subnet.CIDR,
			SpaceName:         spaceName,
			VLANTag:           subnet.VLANTag,
			AvailabilityZone:  firstZone,
		})
		if err != nil {
			return errors.Trace(err)
		}

	}

	// We process FAN subnets separately for clarity.
	cfg, err := st.ModelConfig()
	if err != nil {
		return errors.Trace(err)
	}
	fans, err := cfg.FanConfig()
	if err != nil {
		return errors.Trace(err)
	}
	if len(fans) == 0 {
		return nil
	}

	for _, subnet := range subnets {
		_, subnetNet, err := net.ParseCIDR(subnet.CIDR)
		if err != nil {
			return errors.Trace(err)
		}
		for _, fan := range fans {
			subnetSize, _ := subnetNet.Mask.Size()
			underlaySize, _ := fan.Underlay.Mask.Size()
			// We need to cut a part of fan specific for this physical subnet, eg:
			// for FAN 172.31/16 -> 243/8 and physical subnet 172.31.64/20
			// we get FAN subnet 243.64/12.
			if underlaySize <= subnetSize && fan.Underlay.Contains(subnetNet.IP) {
				id := fmt.Sprintf("%s-INFAN-%d-%d-%d-%d-%d", subnet.ProviderId, subnetNet.IP[0], subnetNet.IP[1], subnetNet.IP[2], subnetNet.IP[3], subnetSize)
				if modelSubnetIds.Contains(id) {
					continue
				}
				overlaySize, _ := fan.Overlay.Mask.Size()
				newOverlaySize := overlaySize + (subnetSize - underlaySize)
				fanSize := uint(underlaySize - overlaySize)
				newFanIP := subnetNet.IP.To4()
				for i := 0; i < 4; i++ {
					newFanIP[i] &^= fan.Underlay.Mask[i]
				}
				numIp := binary.BigEndian.Uint32(newFanIP)
				numIp <<= fanSize
				binary.BigEndian.PutUint32(newFanIP, numIp)
				for i := 0; i < 4; i++ {
					newFanIP[i] += fan.Overlay.IP[i]
				}
				newOverlay := net.IPNet{IP: newFanIP, Mask: net.CIDRMask(newOverlaySize, 32)}
				var firstZone string
				if len(subnet.AvailabilityZones) > 0 {
					firstZone = subnet.AvailabilityZones[0]
				}
				_, err := st.AddSubnet(SubnetInfo{
					ProviderId:        network.Id(id),
					ProviderNetworkId: subnet.ProviderNetworkId,
					CIDR:              newOverlay.String(),
					SpaceName:         spaceName,
					VLANTag:           subnet.VLANTag,
					AvailabilityZone:  firstZone,
					FanLocalUnderlay:  subnet.CIDR,
					FanOverlay:        fan.Overlay.String(),
				})
				if err != nil {
					return errors.Trace(err)
				}

			}
		}
	}

	return nil
}

// SaveSpacesFromProvider loads providerSpaces into state.
// Currently it does not delete removed spaces.
func (st *State) SaveSpacesFromProvider(providerSpaces []network.SpaceInfo) error {
	stateSpaces, err := st.AllSpaces()
	if err != nil {
		return errors.Trace(err)
	}
	modelSpaceMap := make(map[network.Id]*Space)
	spaceNames := make(set.Strings)
	for _, space := range stateSpaces {
		modelSpaceMap[space.ProviderId()] = space
		spaceNames.Add(space.Name())
	}

	// TODO(mfoord): we need to delete spaces and subnets that no longer
	// exist, so long as they're not in use.
	for _, space := range providerSpaces {
		// Check if the space is already in state, in which case we know
		// its name.
		stateSpace, ok := modelSpaceMap[space.ProviderId]
		var spaceTag names.SpaceTag
		if ok {
			spaceName := stateSpace.Name()
			if !names.IsValidSpace(spaceName) {
				// Can only happen if an invalid name is stored
				// in state.
				logger.Errorf("space %q has an invalid name, ignoring", spaceName)
				continue

			}
			spaceTag = names.NewSpaceTag(spaceName)

		} else {
			// The space is new, we need to create a valid name for it
			// in state.
			spaceName := string(space.Name)
			// Convert the name into a valid name that isn't already in
			// use.
			spaceName = network.ConvertSpaceName(spaceName, spaceNames)
			spaceNames.Add(spaceName)
			spaceTag = names.NewSpaceTag(spaceName)
			// We need to create the space.

			logger.Debugf("Adding space %s from provider %s", spaceTag.String(), string(space.ProviderId))
			_, err = st.AddSpace(spaceTag.Id(), space.ProviderId, []string{}, false)
			if err != nil {
				return errors.Trace(err)
			}
		}

		err = st.SaveSubnetsFromProvider(space.Subnets, spaceTag.Id())
		if err != nil {
			return errors.Trace(err)
		}
	}
	return nil
}
