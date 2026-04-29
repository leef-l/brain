/**
 * Card — A single memory game card with 3D flip animation.
 *
 * Props:
 *  - emoji     : string — the emoji displayed on the card face
 *  - index     : number — card index in the board array
 *  - isFlipped : boolean — whether the card is face-up
 *  - isMatched : boolean — whether the card has been matched
 *  - onClick   : (index) => void — click handler
 */
export default function Card({ emoji, index, isFlipped, isMatched, onClick }) {
  const classList = [
    'card',
    isFlipped ? 'card--flipped' : '',
    isMatched ? 'card--matched' : '',
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <button
      className={classList}
      role="gridcell"
      aria-label={isFlipped ? `Card: ${emoji}` : 'Hidden card'}
      aria-pressed={isFlipped}
      onClick={() => onClick(index)}
      tabIndex={isMatched ? -1 : 0}
    >
      <div className="card__inner">
        <div className="card__back">?</div>
        <div className="card__front">{emoji}</div>
      </div>
    </button>
  );
}
