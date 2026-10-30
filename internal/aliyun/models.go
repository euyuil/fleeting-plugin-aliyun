package aliyun

type Group struct {
	ScalingGroupId, RegionId, GroupType, LifecycleState, LaunchTemplateId, LaunchTemplateVersion, ScalingPolicy                                                                         string
	MaxInstanceLifetime                                                                                                                                                                 int
	MinSize, MaxSize, DesiredCapacity, TotalCapacity, PendingCapacity, RemovingCapacity, ProtectedCapacity, StandbyCapacity, StoppedCapacity, PendingWaitCapacity, RemovingWaitCapacity int
	EnableDesiredCapacity                                                                                                                                                               bool
	SpotInstanceRemedy, CompensateWithOnDemand, AzBalance, AutoRebalance, SystemSuspended                                                                                               bool
	OnDemandBaseCapacity, OnDemandPercentageAboveBaseCapacity                                                                                                                           int
	CapacityOptions                                                                                                                                                                     *struct {
		OnDemandBaseCapacity, OnDemandPercentageAboveBaseCapacity int
		CompensateWithOnDemand, SpotAutoReplaceOnDemand           bool
	}
	LaunchTemplateOverrides struct {
		LaunchTemplateOverride []struct {
			WeightedCapacity int
			InstanceType     string
		}
	}
	SuspendedProcesses struct{ SuspendedProcess []string }
	Tags               struct {
		Tag []struct {
			TagKey, TagValue string
			Propagate        bool
		}
	}
}

func (g Group) Tag(key string) string {
	for _, t := range g.Tags.Tag {
		if t.TagKey == key {
			return t.TagValue
		}
	}
	return ""
}

type Member struct {
	InstanceId, ScalingGroupId, LifecycleState, HealthStatus, CreationType, ReplaceStatus, ScalingActivityId, LaunchTemplateId, LaunchTemplateVersion string
	WeightedCapacity                                                                                                                                  int
	Entrusted                                                                                                                                         bool
}
type Instance struct {
	InstanceId, Status, OSType, InstanceType, SpotStrategy, ZoneId, ImageId, InstanceChargeType, AutoReleaseTime string
	PublicIpAddress                                                                                              struct{ IpAddress []string }
	InnerIpAddress                                                                                               struct{ IpAddress []string }
	VpcAttributes                                                                                                struct{ PrivateIpAddress struct{ IpAddress []string } }
	EipAddress                                                                                                   struct{ IpAddress, AllocationId string }
	Tags                                                                                                         struct {
		Tag []struct{ TagKey, TagValue string }
	}
}
type Activity struct{ ScalingActivityId, StatusCode, ScalingGroupId, ErrorCode string }
type Removal struct {
	ScalingActivityId, RequestId string
	IgnoredInstances             struct {
		IgnoredInstance []struct{ InstanceId, Code string }
	}
}
type Template struct {
	LaunchTemplateId   string
	VersionNumber      int
	LaunchTemplateData struct {
		InstanceType, ImageId, SpotStrategy, InstanceChargeType, KeyPairName, ZoneId, RamRoleName, AutoReleaseTime, SecurityGroupId string
		DeletionProtection                                                                                                          bool
		SpotPriceLimit                                                                                                              float64
		InternetMaxBandwidthOut, SpotDuration                                                                                       int
		InternetChargeType                                                                                                          string
		SystemDiskSize                                                                                                              int    `json:"SystemDisk.Size"`
		SystemDiskCategory                                                                                                          string `json:"SystemDisk.Category"`
		SystemDiskPerformanceLevel                                                                                                  string `json:"SystemDisk.PerformanceLevel"`
		SystemDiskDeleteWithInstance                                                                                                *bool  `json:"SystemDisk.DeleteWithInstance"`
		DataDisks                                                                                                                   struct {
			DataDisk []struct{ DeleteWithInstance bool }
		}
		NetworkInterfaces struct {
			NetworkInterface []struct{ DeleteOnRelease bool }
		}
	}
}
