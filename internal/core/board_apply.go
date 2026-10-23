package core

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ApplyBoardEvent incorpora en el store local un delta que LLEGA de otro
// dispositivo (del relay o del transporte P2P, ya descifrado por el daemon
// en el límite de red). Es el lado receptor del flujo que notifyEvent
// publica: lo que otro miembro creó, movió o completó termina legible aquí
// con `bunker board task list`.
//
// Reglas del motor:
//
//   - anti-eco: una aplicación de un evento remoto NUNCA vuelve a notificar
//     el cambio al transporte (dividería el bucle de sincronización contra
//     el propio remitente);
//   - idempotencia por tipo: un task.create repetido o un task.comment ya
//     aplicado no duplican nada (el resultado es el mismo store);
//   - concurrencia optimista local: un delta cuya Version es menor o igual
//     que la del task local y no renueva UpdatedAt se descarta; nunca se
//     cambia una decisión local más nueva por un delta atrasado;
//   - deltas huérfanos: un task.move/done cuyo task no existe localmente se
//     reconstruye completo, porque el payload siempre trae la tarea entera
//     y el relay no garantiza orden entre miembros.
//
// El bool de vuelta dice si el delta efectivamente cambió el store (los
// no-ops por idempotencia o antigüedad no lo hacen).
func (s *BoardService) ApplyBoardEvent(ev BoardEvent) error {
	if _, err := s.store.GetBoard(ev.BoardID); err != nil {
		return fmt.Errorf("core: apply event de board %s: %w", ev.BoardID, ErrBoardNotFound)
	}

	var task Task
	var comment Comment
	switch ev.Type {
	case "task.create":
		if err := json.Unmarshal(ev.Payload, &task); err != nil {
			return fmt.Errorf("core: apply task.create: %w", err)
		}
		if task.BoardID == "" {
			task.BoardID = ev.BoardID
		}
		// El board del evento manda: un payload que declara otro board es
		// confusión de dominios (o un host malintencionado), no un delta.
		if task.BoardID != ev.BoardID {
			return fmt.Errorf("core: apply task.create: el payload declara board %q, se lo entregó el evento del board %q", task.BoardID, ev.BoardID)
		}
		if err := s.store.CreateTask(&task); err != nil {
			if errors.Is(err, ErrTaskExists) {
				return nil // ya aplicado: el store local manda
			}
			return fmt.Errorf("core: apply task.create: %w", err)
		}
		return nil

	case "task.move", "task.done":
		if err := json.Unmarshal(ev.Payload, &task); err != nil {
			return fmt.Errorf("core: apply %s: %w", ev.Type, err)
		}
		// Igual que en create: la tarea venía declarada con el board del
		// evento; sino, indistinguible de un payload forjado.
		if task.BoardID == "" {
			task.BoardID = ev.BoardID
		}
		if task.BoardID != ev.BoardID {
			return fmt.Errorf("core: apply %s: el payload declara board %q, se lo entregó el evento del board %q", ev.Type, task.BoardID, ev.BoardID)
		}
		return s.applyTaskDelta(&task)

	case "task.comment":
		if err := json.Unmarshal(ev.Payload, &comment); err != nil {
			return fmt.Errorf("core: apply task.comment: %w", err)
		}
		if err := s.store.CreateComment(&comment); err != nil {
			if errors.Is(err, ErrCommentExists) {
				return nil // comentario ya aplicado
			}
			return fmt.Errorf("core: apply task.comment: %w", err)
		}
		return nil

	default:
		return fmt.Errorf("core: apply event: tipo %q no es un delta de tablero", ev.Type)
	}
}

// applyTaskDelta fusiona un task.move o task.done: si el task no existe
// localmente lo crea completo (delta huérfano); si existe, respeta la
// concurrencia optimista y solo reescribe cuando el delta es más nuevo
// (Version mayor, o igual con UpdatedAt posterior).
func (s *BoardService) applyTaskDelta(remote *Task) error {
	local, err := s.store.GetTask(remote.ID)
	if err != nil {
		if !errors.Is(err, ErrTaskNotFound) {
			return fmt.Errorf("core: apply delta %s: %w", remote.ID, err)
		}
		// Delta huérfano: el payload trae la tarea entera, recrearla.
		if err := s.store.CreateTask(remote); err != nil {
			if errors.Is(err, ErrTaskExists) {
				return nil // perdi la carrera con otro delta ya aplicado
			}
			return fmt.Errorf("core: recreate %s: %w", remote.ID, err)
		}
		return nil
	}
	// La tarea local ya es del board del evento (se creó con él); si no lo
	// es, hay contaminación previa y el delta no debe agravarla.
	if local.BoardID != remote.BoardID {
		return fmt.Errorf("core: apply delta %s: la tarea local %q pertenece al board %q, el delta dice %q", remote.ID, local.ID, local.BoardID, remote.BoardID)
	}
	if remote.Version < local.Version {
		return nil // atrasado: la decisión local es más nueva
	}
	if remote.Version == local.Version && !remote.UpdatedAt.After(local.UpdatedAt) {
		return nil // misma versión sin avance temporal: no-op
	}
	if err := s.store.UpdateTask(remote); err != nil {
		return fmt.Errorf("core: apply delta %s: %w", remote.ID, err)
	}
	return nil
}
