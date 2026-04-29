import { useState, useEffect, useCallback } from 'react';
import Card from './Card';
import './GameBoard.css';

const EMOJIS = ['🐶', '🐱', '🐸', '🦊', '🐼', '🐨', '🦁', '🐵'];

/**
 * Build a shuffled deck of card objects.
 * Each card: { id, emoji, pairId }
 */
function buildDeck(pairCount = 8) {
  const selected = EMOJIS.slice(0, pairCount);
  const cards = [];
  selected.forEach((emoji, i) => {
    cards.push({ id: i * 2, emoji, pairId: i });
    cards.push({ id: i * 2 + 1, emoji, pairId: i });
  });
  // Fisher-Yates shuffle
  for (let i = cards.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [cards[i], cards[j]] = [cards[j], cards[i]];
  }
  return cards;
}

/**
 * GameBoard — A memory card matching board.
 *
 * Props:
 *  - pairCount       : number — number of pairs on the board
 *  - onScoreChange   : (delta) => void
 *  - onMove          : () => void
 *  - onPairMatched   : () => void
 *  - onGameComplete  : () => void
 *  - resetSignal     : any — triggers board reset when changed
 */
export default function GameBoard({
  pairCount = 8,
  onScoreChange,
  onMove,
  onPairMatched,
  onGameComplete,
  resetSignal,
}) {
  const [cards, setCards] = useState(() => buildDeck(pairCount));
  const [flipped, setFlipped] = useState([]);   // indices currently flipped
  const [matched, setMatched] = useState(new Set());
  const [locked, setLocked] = useState(false);

  // Reset board when resetSignal changes
  useEffect(() => {
    setCards(buildDeck(pairCount));
    setFlipped([]);
    setMatched(new Set());
    setLocked(false);
  }, [resetSignal, pairCount]);

  // Handle card click
  const handleClick = useCallback(
    (idx) => {
      if (locked) return;
      if (flipped.includes(idx)) return;
      if (matched.has(idx)) return;
      if (flipped.length >= 2) return;

      const nextFlipped = [...flipped, idx];
      setFlipped(nextFlipped);

      if (nextFlipped.length === 2) {
        onMove();
        const [a, b] = nextFlipped;
        if (cards[a].pairId === cards[b].pairId) {
          // Match!
          setMatched((prev) => new Set([...prev, a, b]));
          setFlipped([]);
          onScoreChange(10);
          onPairMatched();

          // Check for game complete
          if (matched.size + 2 === cards.length) {
            onGameComplete();
          }
        } else {
          // No match — flip back after delay
          setLocked(true);
          onScoreChange(-2);
          setTimeout(() => {
            setFlipped([]);
            setLocked(false);
          }, 800);
        }
      }
    },
    [flipped, locked, matched, cards, onScoreChange, onMove, onPairMatched, onGameComplete]
  );

  // Determine columns based on pair count
  const columns = cards.length <= 8 ? 4 : cards.length <= 12 ? 4 : 4;

  return (
    <div
      className="game-board"
      role="grid"
      aria-label="Memory game board"
      style={{ '--columns': columns }}
    >
      {cards.map((card, idx) => {
        const isFlipped = flipped.includes(idx) || matched.has(idx);
        const isMatched = matched.has(idx);
        return (
          <Card
            key={card.id}
            emoji={card.emoji}
            index={idx}
            isFlipped={isFlipped}
            isMatched={isMatched}
            onClick={handleClick}
          />
        );
      })}
    </div>
  );
}
