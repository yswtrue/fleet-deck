import type { Node, Workspace, Task, Message, Subtask } from './gen/protocol/index.ts'

export type TaskStatus = Task['status']
export type SubtaskStatus = 'pending' | 'in_progress' | 'done'
export type { Node, Workspace, Task, Message, Subtask }
