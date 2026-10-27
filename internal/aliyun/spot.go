package aliyun

import "errors"

// ValidateSpotSmoke checks the fixed one-worker fixture before any billable write.
func ValidateSpotSmoke(g Group, t Template) error {
	if g.MinSize != 0 || g.MaxSize != 1 || g.DesiredCapacity != 0 || g.TotalCapacity != 0 {
		return errors.New("smoke requires an idle group with MinSize=0, MaxSize=1 and desired/total=0")
	}
	if g.CompensateWithOnDemand || g.OnDemandBaseCapacity != 0 || g.OnDemandPercentageAboveBaseCapacity != 0 || g.SpotInstanceRemedy || g.AzBalance || g.AutoRebalance {
		return errors.New("strict Spot rejects on-demand capacity, fallback and proactive replacement")
	}
	if o := g.CapacityOptions; o != nil && (o.OnDemandBaseCapacity != 0 || o.OnDemandPercentageAboveBaseCapacity != 0 || o.CompensateWithOnDemand || o.SpotAutoReplaceOnDemand) {
		return errors.New("strict Spot rejects mixed capacity options")
	}
	d := t.LaunchTemplateData
	if d.InstanceChargeType != "PostPaid" || (d.SpotStrategy != "SpotAsPriceGo" && d.SpotStrategy != "SpotWithPriceLimit") {
		return errors.New("template must explicitly request Spot")
	}
	if d.RamRoleName != "" || d.InstanceType == "" || d.ImageId == "" || d.KeyPairName == "" || d.ZoneId == "" || d.DeletionProtection {
		return errors.New("smoke requires pinned image, type, zone, key pair and no deletion protection")
	}
	if d.InternetChargeType != "PayByTraffic" || d.InternetMaxBandwidthOut != 1 {
		return errors.New("strict smoke requires explicit traffic billing and 1-Mbps public bandwidth")
	}
	if d.SpotStrategy == "SpotWithPriceLimit" && d.SpotPriceLimit <= 0 {
		return errors.New("SpotWithPriceLimit requires an explicit positive price ceiling")
	}
	if len(g.LaunchTemplateOverrides.LaunchTemplateOverride) > 0 {
		return errors.New("smoke disallows instance-type overrides")
	}
	if d.SystemDiskDeleteWithInstance == nil || !*d.SystemDiskDeleteWithInstance {
		return errors.New("system disk must release with instance")
	}
	for _, disk := range d.DataDisks.DataDisk {
		if !disk.DeleteWithInstance {
			return errors.New("all smoke disks must release with instance")
		}
	}
	for _, eni := range d.NetworkInterfaces.NetworkInterface {
		if !eni.DeleteOnRelease {
			return errors.New("smoke ENIs must release with instance")
		}
	}
	return nil
}
