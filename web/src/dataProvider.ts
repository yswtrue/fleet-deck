import type {
  BaseRecord,
  CreateParams,
  CreateResponse,
  DataProvider,
  DeleteOneParams,
  DeleteOneResponse,
  GetListParams,
  GetListResponse,
  GetOneParams,
  GetOneResponse,
  UpdateParams,
  UpdateResponse,
} from '@refinedev/core'
import { apiClient } from './apiClient.ts'
import type {
  CreateNodeInput,
  CreateTaskInput,
  CreateWorkspaceInput,
  PatchTaskInput,
} from './apiClient.ts'

const asString = (value: unknown): string =>
  typeof value === 'string' ? value : value == null ? '' : String(value)

const asNumber = (value: unknown): number | undefined => (typeof value === 'number' ? value : undefined)

/**
 * Refine dataProvider over the FleetDeck hub REST API. Refine's REST
 * conventions do not apply here, so `resource` selects the endpoint and `meta`
 * carries the arbitrary query params (`node_id`, `workspace_id`, `session_id`,
 * `task_id`, `limit`).
 */
const provider: DataProvider = {
  getApiUrl: () => apiClient.baseUrl,

  getList: async <TData extends BaseRecord = BaseRecord>({
    resource,
    meta,
  }: GetListParams): Promise<GetListResponse<TData>> => {
    let data: unknown[]
    switch (resource) {
      case 'nodes':
        data = await apiClient.listNodes()
        break
      case 'workspaces':
        data = await apiClient.listWorkspaces(asString(meta?.node_id))
        break
      case 'tasks':
        data = await apiClient.listTasks(asString(meta?.workspace_id), asString(meta?.q) || undefined)
        break
      case 'messages':
        data = await apiClient.getMessages(asString(meta?.session_id), asNumber(meta?.limit))
        break
      case 'subtasks':
        data = await apiClient.listSubtasks(asString(meta?.task_id))
        break
      default:
        throw new Error(`dataProvider.getList: unsupported resource "${resource}"`)
    }
    return { data: data as TData[], total: data.length }
  },

  getOne: async <TData extends BaseRecord = BaseRecord>({
    resource,
    id,
  }: GetOneParams): Promise<GetOneResponse<TData>> => {
    switch (resource) {
      case 'nodes':
        return { data: (await apiClient.getNode(asString(id))) as unknown as TData }
      case 'sessions':
        return { data: (await apiClient.getSession(asString(id))) as unknown as TData }
      default:
        throw new Error(`dataProvider.getOne: unsupported resource "${resource}"`)
    }
  },

  create: async <TData extends BaseRecord = BaseRecord, TVariables = {}>({
    resource,
    variables,
  }: CreateParams<TVariables>): Promise<CreateResponse<TData>> => {
    switch (resource) {
      case 'nodes':
        return { data: (await apiClient.createNode(variables as unknown as CreateNodeInput)) as unknown as TData }
      case 'workspaces':
        return {
          data: (await apiClient.createWorkspace(variables as unknown as CreateWorkspaceInput)) as unknown as TData,
        }
      case 'tasks':
        return { data: (await apiClient.createTask(variables as unknown as CreateTaskInput)) as unknown as TData }
      default:
        throw new Error(`dataProvider.create: unsupported resource "${resource}"`)
    }
  },

  update: async <TData extends BaseRecord = BaseRecord, TVariables = {}>({
    resource,
    id,
    variables,
  }: UpdateParams<TVariables>): Promise<UpdateResponse<TData>> => {
    switch (resource) {
      case 'tasks':
        return {
          data: (await apiClient.patchTask(asString(id), variables as unknown as PatchTaskInput)) as unknown as TData,
        }
      default:
        throw new Error(`dataProvider.update: unsupported resource "${resource}"`)
    }
  },

  deleteOne: async <TData extends BaseRecord = BaseRecord, TVariables = {}>({
    resource,
  }: DeleteOneParams<TVariables>): Promise<DeleteOneResponse<TData>> => {
    throw new Error(`dataProvider.deleteOne: not implemented (resource "${resource}")`)
  },
}

export const dataProvider: DataProvider = provider
