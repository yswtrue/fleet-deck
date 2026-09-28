import { Button } from 'antd'
import styles from './PermissionPicker.module.css'
import type { PermissionRequest } from '../permission.ts'

/**
 * isDenyish marks reject/deny/cancel options so the safest choice is visually
 * distinct from the permissive one (label and id are both agent-supplied).
 */
function isDenyish(option: PermissionRequest['options'][number]): boolean {
  return /deny|reject|cancel/i.test(`${option.id} ${option.label}`)
}

/**
 * PermissionPicker renders a structured ACP permission ask (question + one
 * button per option). Buttons are real <button> elements, so Tab + Enter
 * works without extra wiring. Picking an option replies with
 * `[permission_response] option_id="…"` through the normal send path — free
 * text must not stand in for it, because the adapter would fall back to deny.
 */
export function PermissionPicker({
  request,
  onPick,
}: {
  request: PermissionRequest
  onPick: (optionId: string) => void
}) {
  return (
    <section className={styles.card} role="group" aria-label="Permission required">
      <p className={styles.heading}>Permission required</p>
      <p className={styles.question}>{request.question}</p>
      <div className={styles.options}>
        {request.options.map((option) => {
          const deny = isDenyish(option)
          return (
            <Button
              key={option.id}
              danger={deny}
              type={deny ? 'default' : 'primary'}
              onClick={() => onPick(option.id)}
            >
              {option.label}
            </Button>
          )
        })}
      </div>
    </section>
  )
}
