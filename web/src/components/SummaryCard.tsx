import { Card, Statistic } from 'antd'
import { formatCompact } from '../util.ts'

/** Summary tile: compact count on display, exact value in the tooltip. */
export function SummaryCard({ label, value }: { label: string; value: number }) {
  return (
    <Card size="small">
      <Statistic
        title={label}
        value={value}
        formatter={() => <span title={String(value)}>{formatCompact(value)}</span>}
      />
    </Card>
  )
}
