// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"
)

const adminRestartCooldown = 30 * time.Second

func (h *Handler) restartLifecycle(writer http.ResponseWriter) {
	if h.Runtime == nil {
		h.sendError(writer, http.StatusServiceUnavailable, "restart_unavailable", "The active Wormhole runtime is unavailable.")
		return
	}
	h.lifecycleMu.Lock()
	defer h.lifecycleMu.Unlock()
	now := time.Now().UTC()
	if now.Before(h.restartPendingUntil) {
		h.sendJSON(writer, http.StatusAccepted, map[string]any{
			"accepted": true, "alreadyPending": true,
			"retryAfterMs": 1_000, "activeConfigId": h.Runtime.ConfigID,
		})
		return
	}
	if h.scheduleRestart == nil {
		h.sendError(writer, http.StatusServiceUnavailable, "restart_unavailable", "The restart scheduler is unavailable.")
		return
	}
	if err := h.scheduleRestart(h.Runtime.Config.Port); err != nil {
		h.sendError(writer, http.StatusInternalServerError, "restart_schedule_failed", err.Error())
		return
	}
	h.restartPendingUntil = now.Add(adminRestartCooldown)
	h.sendJSON(writer, http.StatusAccepted, map[string]any{
		"accepted": true, "alreadyPending": false,
		"retryAfterMs": 1_000, "activeConfigId": h.Runtime.ConfigID,
		"message": "Restart scheduled. This Admin session will end when the daemon stops; reconnect after Wormhole becomes healthy again.",
	})
}

func scheduleAdminRestart(oldPort int) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, "__admin-restart-helper")
	cmd.Env = append(os.Environ(), "WORMHOLE_ADMIN_OLD_PORT="+strconv.Itoa(oldPort))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	prepareAdminRestart(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	if cmd.Process == nil {
		return errors.New("restart helper did not create a process")
	}
	return cmd.Process.Release()
}
