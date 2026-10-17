package matrix

import (
	"context"
	"errors"

	transport "ouroboros/internal/matrix"
	"ouroboros/plugin"
)

// Keep backend types and lifecycle details behind the optional local contract.
func (c *sdkConnection) VerificationStatus(ctx context.Context) (plugin.GatewayVerification, error) {
	if err := ctx.Err(); err != nil {
		return plugin.GatewayVerification{}, err
	}
	return publicVerification(c.client.VerificationStatus()), nil
}

func publicVerification(snapshot transport.VerificationSnapshot) plugin.GatewayVerification {
	v := plugin.GatewayVerification{Stage: snapshot.Stage, TransactionID: snapshot.TransactionID, DeviceID: snapshot.DeviceID}
	switch snapshot.Stage {
	case "idle":
		v.Message = "Start verification, then compare emojis on your other Matrix device."
	case "requested":
		v.Message = "Accept the verification request on your other Matrix device."
	case "ready", "exchanging":
		v.Stage = "ready"
		v.Message = "Waiting for the emoji comparison."
	case "comparing":
		v.Message = "Compare all seven emojis on both devices before confirming."
	case "confirmed":
		v.Message = "Waiting for the other device to finish verification."
	case "done":
		v.Message = "Device verification completed."
	case "cancelled":
		v.Message = "Device verification was cancelled."
	case "failed":
		v.Message = "Device verification failed; start again from local settings."
	default:
		v.Stage = "unavailable"
		v.Message = "Interactive verification is unavailable on this connection."
	}
	// Never expose backend errors or remote descriptions in the public snapshot.
	if snapshot.Stage == "comparing" {
		for _, emoji := range snapshot.Emojis {
			v.Emojis = append(v.Emojis, emoji.Symbol)
		}
	}
	return v
}

func (c *sdkConnection) VerificationAction(ctx context.Context, action, txn string) error {
	switch action {
	case "start":
		if txn != "" {
			return errors.New("Matrix verification transaction changed")
		}
		_, err := c.client.StartVerification(ctx)
		return err
	case "confirm":
		return c.client.ConfirmVerification(ctx, txn)
	case "cancel":
		return c.client.CancelVerification(ctx, txn)
	default:
		return errors.New("unknown Matrix verification action")
	}
}
