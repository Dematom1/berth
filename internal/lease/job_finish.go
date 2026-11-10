package lease

import (
	"context"
	"fmt"
	"os"
	"time"
)

func (m *Manager) finishJob(dir string, l Lease, groupCleaned bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := m.openStore(ctx, dir)
	if err == nil {
		defer s.Close()
		if groupCleaned {
			if _, ok := s.Leases[l.ID]; ok {
				delete(s.Leases, l.ID)
				err = s.Save()
				if err != nil {
					s.Leases[l.ID] = l
				}
			}
		} else {
			_, err = releaseLease(ctx, s, l.ID, l.Machine, m.options.Adapters)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "berth: job cleanup incomplete; retry berth sweep --confirm:", err)
	}
}
