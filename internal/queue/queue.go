package queue

type DeviceType string

const (
	DeviceTypeSwitch       DeviceType = "switch"
	DeviceTypeLoadBalancer DeviceType = "loadbalancer"
	DeviceTypeFirewall     DeviceType = "firewall"
)

type TaskPriority int

const (
	PriorityLow      TaskPriority = 1
	PriorityNormal   TaskPriority = 3
	PriorityHigh     TaskPriority = 6
	PriorityCritical TaskPriority = 9
)

func GetQueueName(region, az string, deviceType DeviceType) string {
	return "tasks_" + region + "_" + az + "_" + string(deviceType)
}

func GetPriorityQueueName(region, az string, deviceType DeviceType, priority TaskPriority) string {
	var prioritySuffix string
	switch priority {
	case PriorityCritical:
		prioritySuffix = "_critical"
	case PriorityHigh:
		prioritySuffix = "_high"
	case PriorityLow:
		prioritySuffix = "_low"
	default:
		prioritySuffix = ""
	}
	return "tasks_" + region + "_" + az + "_" + string(deviceType) + prioritySuffix
}

func GetCallbackQueueName(region, az string) string {
	return "callbacks_" + region + "_" + az
}

func GetQueueConfig(region, az string, deviceType DeviceType) map[string]int {
	baseQueue := GetQueueName(region, az, deviceType)
	return map[string]int{
		baseQueue + "_critical": int(PriorityCritical),
		baseQueue + "_high":     int(PriorityHigh),
		baseQueue:               int(PriorityNormal),
		baseQueue + "_low":      int(PriorityLow),
	}
}

func GetAllQueuesConfig(region, az string) map[string]int {
	config := make(map[string]int)
	for _, dt := range []DeviceType{DeviceTypeSwitch, DeviceTypeFirewall, DeviceTypeLoadBalancer} {
		for k, v := range GetQueueConfig(region, az, dt) {
			config[k] = v
		}
	}
	return config
}

func GetDeviceTypeForTaskType(taskType string) DeviceType {
	switch taskType {
	case "create_vrf_on_switch", "create_vlan_subinterface", "create_subnet_on_switch", "configure_subnet_routing":
		return DeviceTypeSwitch
	case "create_firewall_zone", "delete_firewall_zone":
		return DeviceTypeFirewall
	case "create_lb_pool", "delete_lb_pool", "configure_lb_listener":
		return DeviceTypeLoadBalancer
	default:
		return DeviceTypeSwitch
	}
}

type TaskOptions struct {
	Priority   TaskPriority
	DeviceType DeviceType
}

func DefaultTaskOptions() *TaskOptions {
	return &TaskOptions{
		Priority:   PriorityNormal,
		DeviceType: DeviceTypeSwitch,
	}
}