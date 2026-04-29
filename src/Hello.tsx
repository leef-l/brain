import React from "react";

interface HelloProps {
  name?: string;
}

const Hello: React.FC<HelloProps> = ({ name = "World" }) => {
  return (
    <div>
      <h1>Hello, {name}!</h1>
    </div>
  );
};

export default Hello;
