package domain

import "time"

// PartnerCourierDTO is `PartnerCourier`: the customer's courier fields plus
// the courier's own ids.
type PartnerCourierDTO struct {
	ID         string `json:"id"`
	ExternalID string `json:"external_id"`
	TrackingCourierDTO
}

// PartnerViewDTO is the partner's live view, without the visibility filter.
type PartnerViewDTO struct {
	DeliveryID         string             `json:"delivery_id"`
	DeliveryModel      Model              `json:"delivery_model"`
	Status             Status             `json:"status"`
	Courier            *PartnerCourierDTO `json:"courier"`
	Pickup             PlaceDTO           `json:"pickup"`
	Dropoff            DropoffDTO         `json:"dropoff"`
	Route              RouteDTO           `json:"route"`
	EtaSeconds         *int               `json:"eta_seconds"`
	EtaWindow          *EtaWindowDTO      `json:"eta_window"`
	EtaState           EtaState           `json:"eta_state"`
	EtaQuality         string             `json:"eta_quality"`
	DistanceRemainingM *float64           `json:"distance_remaining_m"`
	Progress           *float64           `json:"progress"`
	Timeline           []TimelineEntryDTO `json:"timeline"`
	UpdatedAt          time.Time          `json:"updated_at"`
	Version            int64              `json:"version"`
}

// PartnerView projects st for the partner. The courier is null only when none
// is assigned.
func PartnerView(st State, now time.Time) PartnerViewDTO {
	c := buildCommon(st, now, true)
	v := PartnerViewDTO{
		DeliveryID: st.DeliveryID, DeliveryModel: st.Model, Status: st.Status,
		Pickup: c.pickup, Dropoff: c.dropoff, Route: c.route,
		EtaSeconds: c.eta, EtaWindow: c.window, EtaState: c.etaState, EtaQuality: c.quality,
		DistanceRemainingM: c.distance, Progress: c.progress, Timeline: c.timeline,
		UpdatedAt: st.UpdatedAt, Version: st.Version,
	}
	if st.Courier != nil {
		v.Courier = &PartnerCourierDTO{
			ID: st.Courier.ID, ExternalID: st.Courier.ExternalID,
			TrackingCourierDTO: *courierDTO(st, now),
		}
	}
	return v
}
