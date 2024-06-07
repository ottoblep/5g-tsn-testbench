package main

import (
	"flag"
	"fmt"
	"net"
	"time"
)

func main() {
	// The term "port" such as in "port_interface" refers to the outside connections of TSN bridge which normally are ethernet ports
	gtp_tun_opponent_addr_string_flag := flag.String("tunopip", "10.60.0.1", "IP of the other endpoint of the gtp tunnel where ptp packets will be forwarded to (in upstream direction there is no interface ip just the routing matters)")
	enable_unicast_flag := flag.Bool("unicast", false, "Switch operation from multicast to unicast")
	enable_twostep_flag := flag.Bool("twostep", false, "Switch operation from one step to two step")
	port_interface_name_flag := flag.String("portif", "eth1", "Interface of TT bridge outside port (only used with multicast)")
	unicast_addr_string_flag := flag.String("unicastip", "10.100.201.200", "IP of the connected PTP client/server (only used with unicast)")
	flag.Parse()

	gtp_tun_opponent_addr_string = *gtp_tun_opponent_addr_string_flag
	enable_unicast = *enable_unicast_flag
	enable_twostep = *enable_twostep_flag
	port_interface_name = *port_interface_name_flag
	unicast_addr_string = *unicast_addr_string_flag
	last_sync_residence_time = 0
	last_delayreq_residence_time = 0

	TtListen()
}

func TtListen() {
	// Setup Internal 5GS connection
	// IP port 38495 is chosen to communicate between UE and UPF because the multicast is bound to 319 and 320
	fivegs_addr, err := net.ResolveUDPAddr("udp", gtp_tun_opponent_addr_string+":38495")
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	fivegs_listen_addr, _ := net.ResolveUDPAddr("udp", ":38495")

	fivegs_conn, err := net.ListenUDP("udp4", fivegs_listen_addr)
	if err != nil {
		fmt.Println(err.Error())
		return
	}

	defer fivegs_conn.Close()

	if enable_unicast {
		// Setup Unicast Outside Connections
		unicast_general_addr, err := net.ResolveUDPAddr("udp", unicast_addr_string+":320")
		if err != nil {
			fmt.Println(err.Error())
			return
		}
		unicast_event_addr, err := net.ResolveUDPAddr("udp", unicast_addr_string+":319")
		if err != nil {
			fmt.Println(err.Error())
			return
		}

		unicast_listen_general_addr, _ := net.ResolveUDPAddr("udp", ":320")
		unicast_listen_event_addr, _ := net.ResolveUDPAddr("udp", ":319")

		unicast_general_conn, err := net.ListenUDP("udp4", unicast_listen_general_addr)
		if err != nil {
			fmt.Println(err.Error())
			return
		}
		unicast_event_conn, err := net.ListenUDP("udp4", unicast_listen_event_addr)
		if err != nil {
			fmt.Println(err.Error())
			return
		}

		defer unicast_event_conn.Close()
		defer unicast_general_conn.Close()

		go ListenIncoming(unicast_event_conn, fivegs_conn, fivegs_addr)
		go ListenIncoming(unicast_general_conn, fivegs_conn, fivegs_addr)
		go ListenOutgoingUnicast(fivegs_conn, unicast_general_conn, unicast_event_conn, unicast_general_addr, unicast_event_addr)
	} else {
		// Setup Multicast Outside Connections
		port_interface, err := net.InterfaceByName(port_interface_name)
		if err != nil {
			fmt.Println(err.Error())
			return
		}

		peer_general_addr, _ := net.ResolveUDPAddr("udp", "224.0.0.107:320")
		peer_event_addr, _ := net.ResolveUDPAddr("udp", "224.0.0.107:319")
		non_peer_general_addr, _ := net.ResolveUDPAddr("udp", "224.0.1.129:320")
		non_peer_event_addr, _ := net.ResolveUDPAddr("udp", "224.0.1.129:319")

		peer_general_multicast_conn, err := net.ListenMulticastUDP("udp", port_interface, peer_general_addr)
		if err != nil {
			fmt.Println(err.Error())
			return
		}
		peer_event_multicast_conn, err := net.ListenMulticastUDP("udp", port_interface, peer_event_addr)
		if err != nil {
			fmt.Println(err.Error())
			return
		}
		non_peer_general_multicast_conn, err := net.ListenMulticastUDP("udp", port_interface, non_peer_general_addr)
		if err != nil {
			fmt.Println(err.Error())
			return
		}
		non_peer_event_multicast_conn, err := net.ListenMulticastUDP("udp", port_interface, non_peer_event_addr)
		if err != nil {
			fmt.Println(err.Error())
			return
		}

		defer peer_general_multicast_conn.Close()
		defer peer_event_multicast_conn.Close()
		defer non_peer_general_multicast_conn.Close()
		defer non_peer_event_multicast_conn.Close()

		// For some reason both multicast connections pick up all multicast packets (peer and non-peer) instead of only their group as specified in https://pkg.go.dev/net#ListenMulticastUDP
		// As such one listener is sufficient per port
		go ListenIncoming(non_peer_general_multicast_conn, fivegs_conn, fivegs_addr)
		// go ListenIncoming(peer_general_multicast_conn, fivegs_conn, fivegs_addr)
		go ListenIncoming(non_peer_event_multicast_conn, fivegs_conn, fivegs_addr)
		// go ListenIncoming(peer_event_multicast_conn, fivegs_conn, fivegs_addr)
		go ListenOutgoingMulticast(fivegs_conn,
			peer_general_multicast_conn, peer_event_multicast_conn,
			non_peer_general_multicast_conn, non_peer_event_multicast_conn,
			peer_general_addr, peer_event_addr,
			non_peer_general_addr, non_peer_event_addr,
		)
	}

	fmt.Println("TT: initialization complete")

	// TODO: Could use a WaitGroup instead of loop
	for {
		time.Sleep(5 * time.Second)
	}
}
