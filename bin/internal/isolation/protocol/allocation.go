package protocol

func (req ReserveAddressRequest) Validate() error {
	bits := 0
	switch req.Family {
	case "ipv4":
		bits = 32
	case "ipv6":
		bits = 128
	default:
		return ErrInvalidContract
	}
	switch req.Kind {
	case AddressIP:
		if req.Subnet.IsValid() || req.PrefixLength != 0 {
			return ErrInvalidContract
		}
		if req.Reserve {
			if !req.Address.IsValid() || req.Address.Zone() != "" || req.Address.Is4In6() || req.Address.BitLen() != bits {
				return ErrInvalidContract
			}
		} else if req.Address.IsValid() {
			return ErrInvalidContract
		}
	case AddressSubnet:
		if req.Address.IsValid() {
			return ErrInvalidContract
		}
		if req.Reserve {
			if !req.Subnet.IsValid() || req.Subnet.Addr().Is4In6() || req.Subnet.Addr().BitLen() != bits || req.PrefixLength != 0 {
				return ErrInvalidContract
			}
		} else if req.Subnet.IsValid() || int(req.PrefixLength) > bits {
			return ErrInvalidContract
		}
	default:
		return ErrInvalidContract
	}
	return nil
}

func (state AllocationState) Validate(req ReserveAddressRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if state.Kind != req.Kind {
		return ErrInvalidContract
	}
	if req.Kind == AddressSubnet {
		if state.Address.IsValid() || !state.Subnet.IsValid() || state.Subnet != state.Subnet.Masked() || state.Subnet.Addr().Is4In6() {
			return ErrInvalidContract
		}
		if state.Subnet.Addr().Is4() != (req.Family == "ipv4") {
			return ErrInvalidContract
		}
		if req.Reserve {
			if state.Subnet != req.Subnet.Masked() {
				return ErrInvalidContract
			}
			return nil
		}
		if state.Subnet.Bits() != int(req.PrefixLength) {
			return ErrInvalidContract
		}
		return nil
	}
	if !state.Address.IsValid() || state.Address.Zone() != "" || state.Address.Is4In6() || state.Address.Is4() != (req.Family == "ipv4") {
		return ErrInvalidContract
	}
	if req.Reserve {
		if state.Address != req.Address || state.Subnet.IsValid() {
			return ErrInvalidContract
		}
		return nil
	}
	if !state.Subnet.IsValid() || state.Subnet != state.Subnet.Masked() || !state.Subnet.Contains(state.Address) {
		return ErrInvalidContract
	}
	return nil
}
