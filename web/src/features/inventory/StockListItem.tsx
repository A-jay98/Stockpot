import { Link } from 'react-router'
import { ListItem, StateBadge } from '../../ui'
import { FoodIcon } from '../../ui/food-icons'
import type { StockRow } from './hooks'
import { amountLabel, expiryLabel, foodState, isLow } from './status'

/** One stock row: icon, name, amount and place, and the state chip with its date. */
export function StockListItem({ row, now }: { row: StockRow; now: string }) {
  const { item } = row
  const state = foodState(item, now)
  const place = row.location?.name
  return (
    <ListItem
      asChild
      leading={<FoodIcon k={row.icon} size={22} />}
      title={row.name}
      subtitle={[amountLabel(item), place].filter(Boolean).join(' · ')}
      trailing={
        <>
          <StateBadge state={state}>
            {item.expiresOn ? expiryLabel(item.expiresOn, now) : undefined}
          </StateBadge>
          {isLow(item) && state !== 'low' && <StateBadge state="low" />}
        </>
      }
    >
      <Link to={`/inventory/${item.id}`} />
    </ListItem>
  )
}
