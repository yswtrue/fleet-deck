import { isRecord } from '../typeGuards.ts'
import type {
  Event as ProtocolEvent,
  NodeStatusChanged,
  TaskCreated,
  TaskStatusChanged,
  SubtaskUpdated,
  SessionDelta,
  SessionMessage,
  SessionStateChanged,
  SessionThought,
  SessionUsage,
  SessionUserInputRequested,
} from '../gen/protocol/index.ts'

/**
 * DispatchedEvent is the type-narrowed union of the hub -> web events the UI
 * consumes. Each variant pairs the `type` discriminator with its concrete
 * payload, so consumers no longer cast `event.data`.
 */
export type DispatchedEvent =
  | { type: 'node.status_changed'; data: NodeStatusChanged }
  | { type: 'task.created'; data: TaskCreated }
  | { type: 'task.status_changed'; data: TaskStatusChanged }
  | { type: 'subtask.updated'; data: SubtaskUpdated }
  | { type: 'session.delta'; data: SessionDelta }
  | { type: 'session.thought'; data: SessionThought }
  | { type: 'session.message'; data: SessionMessage }
  | { type: 'session.state_changed'; data: SessionStateChanged }
  | { type: 'session.user_input_requested'; data: SessionUserInputRequested }
  | { type: 'session.usage'; data: SessionUsage }

function hasString(obj: Record<string, unknown>, key: string): boolean {
  return typeof obj[key] === 'string'
}

function hasRecord(obj: Record<string, unknown>, key: string): boolean {
  return isRecord(obj[key])
}

function malformed(type: string): null {
  console.warn(`[dispatchEvent] dropping malformed "${type}" event payload`)
  return null
}

/**
 * Validates and narrows one hub Event into a DispatchedEvent.
 *
 * Returns null for unknown event types (forward compatibility) and for
 * malformed payloads (a cheap structural check: the expected id field must be
 * present). No schema-validator dependency — deliberately hand-rolled.
 */
export function dispatchEvent(evt: ProtocolEvent): DispatchedEvent | null {
  if (!isRecord(evt.data)) {
    return malformed(evt.type)
  }
  const data = evt.data

  switch (evt.type) {
    case 'node.status_changed':
      if (!hasString(data, 'node_id') || !hasString(data, 'status')) return malformed(evt.type)
      return { type: 'node.status_changed', data: data as unknown as NodeStatusChanged }

    case 'task.created':
      if (!hasRecord(data, 'task') || !hasString(data.task as Record<string, unknown>, 'id')) {
        return malformed(evt.type)
      }
      return { type: 'task.created', data: data as unknown as TaskCreated }

    case 'task.status_changed':
      if (!hasString(data, 'task_id') || !hasString(data, 'from') || !hasString(data, 'to')) {
        return malformed(evt.type)
      }
      return { type: 'task.status_changed', data: data as unknown as TaskStatusChanged }

    case 'subtask.updated':
      if (
        !hasString(data, 'task_id') ||
        !hasRecord(data, 'subtask') ||
        !hasString(data.subtask as Record<string, unknown>, 'id')
      ) {
        return malformed(evt.type)
      }
      return { type: 'subtask.updated', data: data as unknown as SubtaskUpdated }

    case 'session.delta':
      if (!hasString(data, 'session_id') || !hasString(data, 'text')) return malformed(evt.type)
      return { type: 'session.delta', data: data as unknown as SessionDelta }

    case 'session.thought':
      if (!hasString(data, 'session_id') || !hasString(data, 'text')) return malformed(evt.type)
      return { type: 'session.thought', data: data as unknown as SessionThought }

    case 'session.message':
      if (
        !hasString(data, 'session_id') ||
        !hasRecord(data, 'message') ||
        !hasString(data.message as Record<string, unknown>, 'id')
      ) {
        return malformed(evt.type)
      }
      return { type: 'session.message', data: data as unknown as SessionMessage }

    case 'session.state_changed':
      if (!hasString(data, 'session_id') || !hasString(data, 'status')) return malformed(evt.type)
      return { type: 'session.state_changed', data: data as unknown as SessionStateChanged }

    case 'session.user_input_requested':
      if (!hasString(data, 'session_id') || !hasString(data, 'prompt')) return malformed(evt.type)
      return {
        type: 'session.user_input_requested',
        data: data as unknown as SessionUserInputRequested,
      }

    case 'session.usage':
      if (typeof data.session_id !== 'string' || typeof data.model !== 'string') {
        return malformed(evt.type)
      }
      return { type: 'session.usage', data: data as unknown as SessionUsage }

    default:
      return null
  }
}
